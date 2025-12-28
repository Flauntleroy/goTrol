package service

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"gotrol/internal/bpjs"
	"gotrol/internal/config"
	"gotrol/internal/database"
	"gotrol/internal/models"
	"gotrol/internal/report"
)

// Watcher monitors the database for new entries to process
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

// Start begins watching for new entries
func (w *Watcher) Start() {
	log.Println(" Watching for new entries...")
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	startTime := time.Now()
	processedCount := 0
	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinIdx := 0

	// Spinner animation goroutine
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
			fmt.Println() // New line before stop message
			log.Println("🛑 Watcher stopped")
			return
		case <-ticker.C:
			found := w.checkAndProcess()
			if found > 0 {
				processedCount += found
			}
		}
	}
}

// Stop stops the watcher
func (w *Watcher) Stop() {
	close(w.stopChan)
}

// checkAndProcess checks for new entries and processes them
// Returns number of entries found
func (w *Watcher) checkAndProcess() int {
	entries, err := w.fetchPendingEntries()
	if err != nil {
		log.Printf("❌ Error fetching entries: %v", err)
		return 0
	}

	if len(entries) == 0 {
		return 0
	}

	log.Printf("📥 Found %d new entry(ies) with status \"Sudah\"", len(entries))

	for _, entry := range entries {
		w.processEntry(entry)
	}

	log.Println("⏳ Watching for new entries...")
	return len(entries)
}

// fetchPendingEntries gets entries with status_kirim = 'Sudah' and JB = BPJS that haven't been processed
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
			AND NOT EXISTS (
				SELECT 1 FROM mlite_antrian_referensi_taskid t 
				WHERE t.nomor_referensi = mar.nomor_referensi 
				AND t.status = 'Sudah'
				AND t.taskid = 5
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

// processEntry processes a single entry - auto order + update waktu
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

	// Step 1: Get current task times
	tasks, err := w.fetchTaskTimes(entry)
	if err != nil {
		log.Printf("   └── ❌ Error fetching task times: %v", err)
		result.Error = err.Error()
		w.reportStore.SaveResult(result)
		return
	}

	// Step 2: Apply auto order logic
	orderedTasks := w.processor.ProcessTasks(tasks)
	log.Println("   ├── Auto Order: Task 1-7 ordered ✓")
	result.AutoOrderDone = true

	// Step 3: Save to database
	if err := w.saveTaskIDs(entry, orderedTasks); err != nil {
		log.Printf("   └── ❌ Error saving task IDs: %v", err)
		result.Error = err.Error()
		w.reportStore.SaveResult(result)
		return
	}
	log.Println("   ├── Saved to mlite_antrian_referensi_taskid ✓")

	// Step 4: Send to BPJS
	allSuccess := true
	for i := 0; i < 7; i++ {
		taskNum := i + 1
		if orderedTasks[i] == nil {
			result.Tasks[taskNum] = models.TaskResult{
				Waktu:      "",
				BPJSStatus: "skipped",
			}
			continue
		}

		waktuMs := TimeToMillis(orderedTasks[i])
		resp, err := w.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMs)

		taskResult := models.TaskResult{
			Waktu: FormatTime(orderedTasks[i]),
		}

		if err != nil {
			taskResult.BPJSStatus = "error"
			taskResult.Message = err.Error()
			allSuccess = false
			log.Printf("   ├── BPJS Task %d: ❌ Error: %v", taskNum, err)
		} else {
			taskResult.BPJSCode = resp.Metadata.Code
			if resp.IsSuccess() {
				taskResult.BPJSStatus = "success"
				log.Printf("   ├── BPJS Task %d: 200 OK ✓", taskNum)
				// Update status in database
				w.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
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

// fetchTaskTimes gets current task times from various sources
func (w *Watcher) fetchTaskTimes(entry models.AntrianReferensi) ([7]*time.Time, error) {
	var tasks [7]*time.Time

	// Try to get from existing taskid table first
	existingTasks, err := w.getExistingTaskIDs(entry.NomorReferensi)
	if err == nil && len(existingTasks) > 0 {
		for _, t := range existingTasks {
			if t.TaskID >= 1 && t.TaskID <= 7 && t.Waktu > 0 {
				tm := MillisToTime(t.Waktu)
				tasks[t.TaskID-1] = tm
			}
		}
		return tasks, nil
	}

	// Otherwise, fetch from source tables
	return w.getTaskTimesFromSources(entry)
}

// getExistingTaskIDs fetches existing task IDs from database
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

// getTaskTimesFromSources fetches task times from source tables (loket, mutasi_berkas, etc.)
// Falls back to reg_periksa datetime if no data found (like PHP does)
func (w *Watcher) getTaskTimesFromSources(entry models.AntrianReferensi) ([7]*time.Time, error) {
	var tasks [7]*time.Time
	loc := time.Local

	// Extract date part from TanggalPeriksa (may be ISO format like 2025-12-24T00:00:00+08:00)
	tanggal := entry.TanggalPeriksa
	if len(tanggal) >= 10 {
		tanggal = tanggal[:10] // Take only YYYY-MM-DD part
	}

	// First, get default time from reg_periksa using no_rawat (tgl_registrasi + jam_reg)
	var tglReg, jamReg sql.NullString
	var defaultTime *time.Time

	if entry.NoRawat != "" {
		err := w.db.DB.QueryRow(`
			SELECT tgl_registrasi, jam_reg FROM reg_periksa 
			WHERE no_rawat = ?
		`, entry.NoRawat).Scan(&tglReg, &jamReg)
		if err == nil && tglReg.Valid && jamReg.Valid {
			// Also extract date part from tglReg
			tglStr := tglReg.String
			if len(tglStr) >= 10 {
				tglStr = tglStr[:10]
			}
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", tglStr+" "+jamReg.String, loc); err == nil {
				defaultTime = &t
			}
		}
	}

	// If no default time, try parsing from TanggalPeriksa with 08:00
	if defaultTime == nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", tanggal+" 08:00:00", loc); err == nil {
			defaultTime = &t
		}
	}

	// Task 1 & 2: from mlite_antrian_loket OR default
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
	// Fallback to default time
	if tasks[0] == nil && defaultTime != nil {
		t := *defaultTime
		tasks[0] = &t
	}
	if tasks[1] == nil && defaultTime != nil {
		t := *defaultTime
		tasks[1] = &t
	}

	// Task 3: from mutasi_berkas.dikirim (NO FALLBACK - only use actual data)
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

	// Task 4: from mutasi_berkas.diterima (NO FALLBACK - only use actual data)
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

	// Task 5: from pemeriksaan_ralan (NO FALLBACK - only use actual data)
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

	// Task 6 & 7: from resep_obat - NO DEFAULT (optional tasks)
	var tglPeresepan, jam, jamPeresepan sql.NullString
	err = w.db.DB.QueryRow(`
		SELECT tgl_peresepan, jam, jam_peresepan FROM resep_obat 
		WHERE no_rawat = ?
	`, entry.NoRawat).Scan(&tglPeresepan, &jam, &jamPeresepan)
	if err == nil {
		// Extract date part from tglPeresepan
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

	return tasks, nil
}

// saveTaskIDs saves the processed task IDs to database
func (w *Watcher) saveTaskIDs(entry models.AntrianReferensi, tasks [7]*time.Time) error {
	// Extract date part from TanggalPeriksa
	tanggal := entry.TanggalPeriksa
	if len(tanggal) >= 10 {
		tanggal = tanggal[:10]
	}

	// Delete existing
	_, err := w.db.DB.Exec("DELETE FROM mlite_antrian_referensi_taskid WHERE nomor_referensi = ?", entry.NomorReferensi)
	if err != nil {
		return err
	}

	keterangan := []string{
		"Mulai tunggu admisi.",
		"Mulai pelayanan admisi.",
		"Selesai pelayanan admisi atau mulai tunggu poli.",
		"Mulai pelayanan poli.",
		"Selesai pelayanan poli.",
		"Mulai pelayanan apotek.",
		"Selesai pelayanan apotek.",
	}

	// Insert new
	for i := 0; i < 7; i++ {
		if tasks[i] == nil {
			continue
		}
		waktuMs := TimeToMillis(tasks[i])
		_, err := w.db.DB.Exec(`
			INSERT INTO mlite_antrian_referensi_taskid 
			(tanggal_periksa, nomor_referensi, taskid, waktu, status, keterangan)
			VALUES (?, ?, ?, ?, 'Belum', ?)
		`, tanggal, entry.NomorReferensi, i+1, waktuMs, keterangan[i])
		if err != nil {
			return err
		}
	}

	return nil
}

// updateTaskStatus updates the status of a task in database
func (w *Watcher) updateTaskStatus(nomorReferensi string, taskID int, status string) {
	_, _ = w.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid 
		SET status = ? 
		WHERE nomor_referensi = ? AND taskid = ?
	`, status, nomorReferensi, taskID)
}
