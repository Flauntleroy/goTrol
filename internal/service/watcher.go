package service

import (
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"gotrol/internal/bpjs"
	"gotrol/internal/config"
	"gotrol/internal/database"
	"gotrol/internal/models"
	"gotrol/internal/report"
)

type Watcher struct {
	db           *database.MySQL
	bpjsClient   *bpjs.Client
	processor    *AutoOrderProcessor
	reportStore  *report.Store
	pollInterval time.Duration
	kdPjBPJS     string
	stopChan     chan struct{}
}

func NewWatcher(db *database.MySQL, creds *config.BPJSCredentials, reportStore *report.Store, pollInterval time.Duration) *Watcher {
	return &Watcher{
		db:           db,
		bpjsClient:   bpjs.NewClient(creds),
		processor:    NewAutoOrderProcessor(),
		reportStore:  reportStore,
		pollInterval: pollInterval,
		kdPjBPJS:     creds.KdPjBPJS,
		stopChan:     make(chan struct{}),
	}
}

func (w *Watcher) Start() {
	log.Println(" Watching for new entries...")
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	startTime := time.Now()
	processedCount := 0
	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0

	spinnerTicker := time.NewTicker(100 * time.Millisecond)
	defer spinnerTicker.Stop()

	go func() {
		for range spinnerTicker.C {
			uptime := time.Since(startTime).Round(time.Second)
			fmt.Printf("\r%s Waiting for new data to process... | Uptime: %s | Processed: %d   ", spinner[spinIdx], uptime, processedCount)
			spinIdx = (spinIdx + 1) % len(spinner)
		}
	}()

	for {
		select {
		case <-w.stopChan:
			fmt.Println()
			log.Println("Watcher stopped")
			return
		case <-ticker.C:
			found := w.checkAndProcess()
			if found > 0 {
				processedCount += found
			}
		}
	}
}

func (w *Watcher) Stop() {
	close(w.stopChan)
}

func (w *Watcher) checkAndProcess() int {
	entries, err := w.fetchPendingEntries()
	if err != nil {
		log.Printf("  Error fetching entries: %v", err)
		return 0
	}

	if len(entries) == 0 {
		return 0
	}

	log.Printf("📥 Found %d new entry(ies) with status \"Sudah\"", len(entries))

	for _, entry := range entries {
		w.processEntry(entry)
	}

	log.Println(" Watching for new entries...")
	return len(entries)
}

func (w *Watcher) fetchPendingEntries() ([]models.AntrianReferensi, error) {
	today := time.Now().Format("2006-01-02")

	query := `
		SELECT 
			mar.tanggal_periksa,
			mar.no_rkm_medis,
			mar.nomor_kartu,
			mar.nomor_referensi,
			mar.kodebooking,
			COALESCE(mar.jenis_kunjungan, '') as jenis_kunjungan,
			mar.status_kirim,
			COALESCE(mar.keterangan, '') as keterangan,
			COALESCE(p.nm_pasien, '') as nm_pasien,
			COALESCE(rp.no_rawat, '') as no_rawat,
			COALESCE(pj.png_jawab, '') as png_jawab
		FROM mlite_antrian_referensi mar
		LEFT JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		LEFT JOIN pasien p ON mar.no_rkm_medis = p.no_rkm_medis
		LEFT JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.status_kirim = 'Sudah'
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
			AND (
				-- No task records yet
				NOT EXISTS (
					SELECT 1 FROM mlite_antrian_referensi_taskid t 
					WHERE t.nomor_referensi = mar.nomor_referensi
				)
				OR
				-- Has incomplete tasks (1-5 not all Sudah)
				(SELECT COUNT(*) FROM mlite_antrian_referensi_taskid t 
				 WHERE t.nomor_referensi = mar.nomor_referensi 
				   AND t.taskid IN (1,2,3,4,5) 
				   AND t.status = 'Sudah') < 5
			)
		ORDER BY rp.jam_reg ASC
	`

	rows, err := w.db.DB.Query(query, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.AntrianReferensi
	for rows.Next() {
		var e models.AntrianReferensi
		if err := rows.Scan(
			&e.TanggalPeriksa,
			&e.NoRkmMedis,
			&e.NomorKartu,
			&e.NomorReferensi,
			&e.KodeBooking,
			&e.JenisKunjungan,
			&e.StatusKirim,
			&e.Keterangan,
			&e.NamaPasien,
			&e.NoRawat,
			&e.PngJawab,
		); err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		entries = append(entries, e)
	}

	return entries, nil
}

func (w *Watcher) processEntry(entry models.AntrianReferensi) {
	startTime := time.Now()
	log.Printf("🔄 Processing: %s - %s (Ref: %s)", entry.NoRkmMedis, entry.NamaPasien, entry.NomorReferensi)

	result := models.ProcessResult{
		NomorReferensi: entry.NomorReferensi,
		KodeBooking:    entry.KodeBooking,
		NoRkmMedis:     entry.NoRkmMedis,
		NamaPasien:     entry.NamaPasien,
		NoRawat:        entry.NoRawat,
		ProcessedAt:    time.Now(),
		Tasks:          make(map[int]models.TaskResult),
	}

	tasks, generated, err := w.fetchTaskTimes(entry)
	if err != nil {
		log.Printf("   └──  Error fetching task times: %v", err)
		result.Error = err.Error()
		w.reportStore.SaveResult(result)
		return
	}

	orderedTasks := w.processor.ProcessTasks(tasks)
	log.Println("   ├── Auto Order: Task 1-7 ordered ")
	result.AutoOrderDone = true

	if err := w.saveTaskIDs(entry, orderedTasks, generated); err != nil {
		log.Printf("   └──  Error saving task IDs: %v", err)
		result.Error = err.Error()
		w.reportStore.SaveResult(result)
		return
	}
	log.Println("   ├── Saved to mlite_antrian_referensi_taskid ")

	allSuccess := true
	lastAcceptedMs := w.getMaxSentTime(entry.NomorReferensi)
	completedTasks := w.getCompletedTaskIDs(entry.NomorReferensi)

	for i := 0; i < 7; i++ {
		taskNum := i + 1

		if completedTasks[taskNum] {
			result.Tasks[taskNum] = models.TaskResult{
				Waktu:      "",
				BPJSStatus: "skipped",
				Message:    "Already Sudah",
			}
			log.Printf("   ├── Task %d: Skipped (already Sudah)", taskNum)
			continue
		}

		if orderedTasks[i] == nil {
			result.Tasks[taskNum] = models.TaskResult{
				Waktu:      "",
				BPJSStatus: "skipped",
			}
			continue
		}

		waktuMs := TimeToMillis(orderedTasks[i])
		if lastAcceptedMs > 0 && waktuMs <= lastAcceptedMs {
			waktuMs = lastAcceptedMs + 60_000
			w.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMs)
		}
		resp, err := w.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMs)

		taskResult := models.TaskResult{
			Waktu: FormatTime(orderedTasks[i]),
		}

		if err != nil {
			taskResult.BPJSStatus = "error"
			taskResult.Message = err.Error()
			allSuccess = false
			log.Printf("   ├── BPJS Task %d:  Error: %v", taskNum, err)
		} else {
			taskResult.BPJSCode = resp.Metadata.Code
			msgLower := strings.ToLower(resp.Metadata.Message)

			if resp.IsSuccess() {
				taskResult.BPJSStatus = "success"
				log.Printf("   ├── BPJS Task %d: 200 OK ", taskNum)
				w.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
				lastAcceptedMs = waktuMs
			} else if resp.Metadata.Code == 208 && strings.Contains(msgLower, "sudah ada") {

				taskResult.BPJSStatus = "success"
				taskResult.Message = resp.Metadata.Message
				log.Printf("   ├── BPJS Task %d: 208 Sudah ada ", taskNum)
				w.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
				lastAcceptedMs = waktuMs
			} else if strings.Contains(msgLower, "tidak boleh kurang atau sama") {
				delta := int64(3_600_000)
				waktuMsRetry := maxInt64(waktuMs, lastAcceptedMs) + delta
				nextMinMs := int64(0)
				for k := i + 1; k < 7; k++ {
					if orderedTasks[k] != nil {
						m := TimeToMillis(orderedTasks[k])
						if nextMinMs == 0 || m < nextMinMs {
							nextMinMs = m
						}
					}
				}
				if nextMinMs > 0 && waktuMsRetry >= nextMinMs {
					orderedTasks = w.adjustForward(entry, orderedTasks, i, waktuMsRetry)
				}
				resp2, err2 := w.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMsRetry)
				if err2 == nil && resp2.IsSuccess() {
					taskResult.BPJSCode = resp2.Metadata.Code
					taskResult.BPJSStatus = "success"
					taskResult.Message = ""
					taskResult.Waktu = time.UnixMilli(waktuMsRetry).Format("2006-01-02 15:04:05")
					w.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMsRetry)
					w.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
					log.Printf("   ├── BPJS Task %d: 200 OK (retry +1h)", taskNum)
					lastAcceptedMs = waktuMsRetry
				} else if err2 == nil && resp2.Metadata.Code == 208 && strings.Contains(strings.ToLower(resp2.Metadata.Message), "sudah ada") {
					taskResult.BPJSCode = resp2.Metadata.Code
					taskResult.BPJSStatus = "success"
					taskResult.Message = resp2.Metadata.Message
					w.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
					log.Printf("   ├── BPJS Task %d: 208 Sudah ada ", taskNum)
					lastAcceptedMs = waktuMsRetry
				} else {
					taskResult.BPJSStatus = "failed"
					taskResult.Message = resp.Metadata.Message
					allSuccess = false
					log.Printf("   ├── BPJS Task %d: %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
				}
			} else {
				taskResult.BPJSStatus = "failed"
				taskResult.Message = resp.Metadata.Message
				allSuccess = false
				log.Printf("   ├── BPJS Task %d: %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
			}
		}
		result.Tasks[taskNum] = taskResult
	}

	result.UpdateWaktuDone = allSuccess
	elapsed := time.Since(startTime)
	log.Printf("   └── Complete! (%.1fs)", elapsed.Seconds())

	w.reportStore.SaveResult(result)
}

func (w *Watcher) fetchTaskTimes(entry models.AntrianReferensi) ([7]*time.Time, [7]bool, error) {
	var tasks [7]*time.Time
	var generated [7]bool

	existingTasks, err := w.getExistingTaskIDs(entry.NomorReferensi)
	if err == nil && len(existingTasks) > 0 {
		for _, t := range existingTasks {
			if t.TaskID >= 1 && t.TaskID <= 7 && t.Waktu > 0 {
				tm := MillisToTime(t.Waktu)
				tasks[t.TaskID-1] = tm
			}
		}
	}

	srcTasks, srcGenerated, err := w.getTaskTimesFromSources(entry)
	if err != nil {
		return tasks, generated, err
	}
	for i := 0; i < 7; i++ {
		if tasks[i] == nil && srcTasks[i] != nil {
			tasks[i] = srcTasks[i]
			generated[i] = srcGenerated[i]
		}
	}
	return tasks, generated, nil
}

func (w *Watcher) getExistingTaskIDs(nomorReferensi string) ([]models.TaskID, error) {
	query := `
		SELECT tanggal_periksa, nomor_referensi, taskid, waktu, status, keterangan
		FROM mlite_antrian_referensi_taskid
		WHERE nomor_referensi = ?
	`
	rows, err := w.db.DB.Query(query, nomorReferensi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []models.TaskID
	for rows.Next() {
		var t models.TaskID
		if err := rows.Scan(&t.TanggalPeriksa, &t.NomorReferensi, &t.TaskID, &t.Waktu, &t.Status, &t.Keterangan); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (w *Watcher) getTaskTimesFromSources(entry models.AntrianReferensi) ([7]*time.Time, [7]bool, error) {
	var tasks [7]*time.Time
	var generated [7]bool
	loc := time.Local

	tanggal := entry.TanggalPeriksa
	if len(tanggal) >= 10 {
		tanggal = tanggal[:10]
	}

	var tglReg, jamReg sql.NullString
	var defaultTime *time.Time

	if entry.NoRawat != "" {
		err := w.db.DB.QueryRow(`
			SELECT tgl_registrasi, jam_reg FROM reg_periksa 
			WHERE no_rawat = ?
		`, entry.NoRawat).Scan(&tglReg, &jamReg)
		if err == nil && tglReg.Valid && jamReg.Valid {

			tglStr := tglReg.String
			if len(tglStr) >= 10 {
				tglStr = tglStr[:10]
			}
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tglStr+" "+jamReg.String, loc); err == nil {
				defaultTime = &t
			}
		}
	}

	if defaultTime == nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", tanggal+" 08:00:00", loc); err == nil {
			defaultTime = &t
		}
	}

	var startTime, endTime sql.NullString
	var err error
	err = w.db.DB.QueryRow(`
		SELECT start_time, end_time 
		FROM mlite_antrian_loket 
		WHERE no_rkm_medis = ? AND postdate = ?
	`, entry.NoRkmMedis, tanggal).Scan(&startTime, &endTime)
	if err == nil {
		if startTime.Valid && startTime.String != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tanggal+" "+startTime.String, loc); err == nil {
				tasks[0] = &t
			}
		}
		if endTime.Valid && endTime.String != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tanggal+" "+endTime.String, loc); err == nil {
				tasks[1] = &t
			}
		}
	}

	if tasks[0] == nil && defaultTime != nil {
		t := *defaultTime
		tasks[0] = &t
	}
	if tasks[1] == nil && defaultTime != nil {
		t := *defaultTime
		tasks[1] = &t
	}

	var dikirim sql.NullString
	err = w.db.DB.QueryRow(`
		SELECT dikirim FROM mutasi_berkas 
		WHERE no_rawat = ? AND dikirim != '0000-00-00 00:00:00'
	`, entry.NoRawat).Scan(&dikirim)
	if err == nil && dikirim.Valid && dikirim.String != "" && dikirim.String != "0000-00-00 00:00:00" {
		dikirimStr := dikirim.String
		if len(dikirimStr) >= 10 && dikirimStr[10:11] == "T" {
			dikirimStr = dikirimStr[:10] + " " + dikirimStr[11:19]
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", dikirimStr, loc); err == nil {
			tasks[2] = &t
		}
	}

	var diterima sql.NullString
	err = w.db.DB.QueryRow(`
		SELECT diterima FROM mutasi_berkas 
		WHERE no_rawat = ? AND diterima != '0000-00-00 00:00:00'
	`, entry.NoRawat).Scan(&diterima)
	if err == nil && diterima.Valid && diterima.String != "" && diterima.String != "0000-00-00 00:00:00" {
		diterimaStr := diterima.String
		if len(diterimaStr) >= 10 && diterimaStr[10:11] == "T" {
			diterimaStr = diterimaStr[:10] + " " + diterimaStr[11:19]
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", diterimaStr, loc); err == nil {
			tasks[3] = &t
		}
	}

	var tglPerawatan, jamRawat sql.NullString
	err = w.db.DB.QueryRow(`
		SELECT tgl_perawatan, jam_rawat FROM pemeriksaan_ralan 
		WHERE no_rawat = ?
	`, entry.NoRawat).Scan(&tglPerawatan, &jamRawat)
	if err == nil && tglPerawatan.Valid && jamRawat.Valid {
		tglStr := tglPerawatan.String
		if len(tglStr) >= 10 {
			tglStr = tglStr[:10]
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", tglStr+" "+jamRawat.String, loc); err == nil {
			tasks[4] = &t
		}
	}

	var tglPeresepan, jam, jamPeresepan sql.NullString
	err = w.db.DB.QueryRow(`
		SELECT tgl_peresepan, jam, jam_peresepan FROM resep_obat 
		WHERE no_rawat = ?
	`, entry.NoRawat).Scan(&tglPeresepan, &jam, &jamPeresepan)
	if err == nil {

		tglStr := ""
		if tglPeresepan.Valid && tglPeresepan.String != "" {
			tglStr = tglPeresepan.String
			if len(tglStr) >= 10 {
				tglStr = tglStr[:10]
			}
		}
		if tglStr != "" && jamPeresepan.Valid && jamPeresepan.String != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tglStr+" "+jamPeresepan.String, loc); err == nil {
				tasks[5] = &t
			}
		}
		if tglStr != "" && jam.Valid && jam.String != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tglStr+" "+jam.String, loc); err == nil {
				tasks[6] = &t
			}
		}
	}

	if tasks[2] == nil {
		var base *time.Time
		if tasks[1] != nil {
			base = tasks[1]
		} else if tasks[0] != nil {
			base = tasks[0]
		} else if defaultTime != nil {
			base = defaultTime
		}
		if base != nil {
			offset := rand.Intn(5) + 1
			t := base.Add(time.Duration(offset) * time.Minute)
			tasks[2] = &t
			generated[2] = true
		}
	}
	if tasks[3] == nil {
		if tasks[2] != nil {
			offset := rand.Intn(10) + 1
			t := tasks[2].Add(time.Duration(offset) * time.Minute)
			if tasks[4] != nil {
				if t.After(*tasks[4]) || t.Equal(*tasks[4]) {
					maxAllowed := int(tasks[4].Sub(*tasks[2]).Minutes()) - 1
					if maxAllowed < 1 {
						maxAllowed = 1
					}
					t = tasks[2].Add(time.Duration(maxAllowed) * time.Minute)
				}
			}
			tasks[3] = &t
			generated[3] = true
		} else if defaultTime != nil {
			offset := rand.Intn(5) + 3
			t := defaultTime.Add(time.Duration(offset) * time.Minute)
			tasks[3] = &t
			generated[3] = true
		}
	}
	if tasks[4] == nil {
		if tasks[3] != nil {
			offset := rand.Intn(16) + 10
			t := tasks[3].Add(time.Duration(offset) * time.Minute)
			tasks[4] = &t
			generated[4] = true
		} else if tasks[2] != nil {
			offset := rand.Intn(16) + 10
			t := tasks[2].Add(time.Duration(offset) * time.Minute)
			tasks[4] = &t
			generated[4] = true
		} else if defaultTime != nil {
			offset := rand.Intn(11) + 10
			t := defaultTime.Add(time.Duration(offset) * time.Minute)
			tasks[4] = &t
			generated[4] = true
		}
	}

	var gens []int
	for i := 2; i <= 4; i++ {
		if generated[i] {
			gens = append(gens, i+1)
		}
	}
	if len(gens) > 0 {
		log.Printf("   ├── Fallback generator activated for Task %v", gens)
	}

	return tasks, generated, nil
}

func (w *Watcher) saveTaskIDs(entry models.AntrianReferensi, tasks [7]*time.Time, generated [7]bool) error {

	tanggal := entry.TanggalPeriksa
	if len(tanggal) >= 10 {
		tanggal = tanggal[:10]
	}

	completedTasks := w.getCompletedTaskIDs(entry.NomorReferensi)

	keterangan := []string{
		"Mulai tunggu admisi.",
		"Mulai pelayanan admisi.",
		"Selesai pelayanan admisi atau mulai tunggu poli.",
		"Mulai pelayanan poli.",
		"Selesai pelayanan poli.",
		"Mulai pelayanan apotek.",
		"Selesai pelayanan apotek.",
	}

	for i := 0; i < 7; i++ {
		taskNum := i + 1

		if completedTasks[taskNum] {
			continue
		}

		if tasks[i] == nil {
			continue
		}

		waktuMs := TimeToMillis(tasks[i])
		ket := keterangan[i]
		if generated[i] {
			ket = ket + " [generated]"
		}

		_, err := w.db.DB.Exec(`
			INSERT INTO mlite_antrian_referensi_taskid 
			(tanggal_periksa, nomor_referensi, taskid, waktu, status, keterangan)
			VALUES (?, ?, ?, ?, 'Belum', ?)
			ON DUPLICATE KEY UPDATE 
				waktu = IF(status != 'Sudah', VALUES(waktu), waktu),
				keterangan = IF(status != 'Sudah', VALUES(keterangan), keterangan)
		`, tanggal, entry.NomorReferensi, taskNum, waktuMs, ket)
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *Watcher) updateTaskStatus(nomorReferensi string, taskID int, status string) {
	_, _ = w.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid 
		SET status = ? 
		WHERE nomor_referensi = ? AND taskid = ?
	`, status, nomorReferensi, taskID)
}

func (w *Watcher) updateTaskWaktu(nomorReferensi string, taskID int, waktuMs int64) {
	_, _ = w.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid 
		SET waktu = ? 
		WHERE nomor_referensi = ? AND taskid = ? AND status != 'Sudah'
	`, waktuMs, nomorReferensi, taskID)
}

func (w *Watcher) getMaxSentTime(nomorReferensi string) int64 {
	var maxWaktu sql.NullInt64
	_ = w.db.DB.QueryRow(`
		SELECT COALESCE(MAX(waktu), 0) FROM mlite_antrian_referensi_taskid 
		WHERE nomor_referensi = ? AND status = 'Sudah'
	`, nomorReferensi).Scan(&maxWaktu)
	if maxWaktu.Valid {
		return maxWaktu.Int64
	}
	return 0
}

func (w *Watcher) getCompletedTaskIDs(nomorReferensi string) map[int]bool {
	result := make(map[int]bool)
	rows, err := w.db.DB.Query(`
		SELECT taskid FROM mlite_antrian_referensi_taskid 
		WHERE nomor_referensi = ? AND status = 'Sudah'
	`, nomorReferensi)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var taskID int
		if rows.Scan(&taskID) == nil {
			result[taskID] = true
		}
	}
	return result
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (w *Watcher) adjustForward(entry models.AntrianReferensi, ordered [7]*time.Time, startIdx int, baseMs int64) [7]*time.Time {
	t := time.UnixMilli(baseMs)
	for k := startIdx + 1; k < 7; k++ {
		if ordered[k] != nil {
			m := TimeToMillis(ordered[k])
			if m <= baseMs {
				r := rand.Intn(5) + 1
				newT := t.Add(time.Duration(r) * time.Minute)
				if k+1 < 7 && ordered[k+1] != nil {
					next := *ordered[k+1]
					if newT.After(next) || newT.Equal(next) {
						maxAllowed := int(next.Sub(t).Minutes()) - 1
						if maxAllowed < 1 {
							maxAllowed = 1
						}
						newT = t.Add(time.Duration(maxAllowed) * time.Minute)
					}
				}
				ordered[k] = &newT
				w.updateTaskWaktu(entry.NomorReferensi, k+1, newT.UnixMilli())
				t = newT
				baseMs = newT.UnixMilli()
			} else {
				t = *ordered[k]
				baseMs = TimeToMillis(&t)
			}
		}
	}
	return ordered
}
