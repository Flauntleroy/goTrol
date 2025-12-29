package service

import (
	"database/sql"
	"math/rand"
	"log"
	"strings"
	"time"

	"gotrol/internal/bpjs"
	"gotrol/internal/config"
	"gotrol/internal/database"
	"gotrol/internal/models"
	"gotrol/internal/report"
)

// BatchHandler handles manual batch operations
type BatchHandler struct {
	db          *database.MySQL
	bpjsClient  *bpjs.Client
	processor   *AutoOrderProcessor
	reportStore *report.Store
}

func NewBatchHandler(db *database.MySQL, creds *config.BPJSCredentials, reportStore *report.Store) *BatchHandler {
	return &BatchHandler{
		db:          db,
		bpjsClient:  bpjs.NewClient(creds),
		processor:   NewAutoOrderProcessor(),
		reportStore: reportStore,
	}
}

// BatchAutoOrder processes auto order for all BPJS patients on a date (without sending to BPJS)
func (b *BatchHandler) BatchAutoOrder(date string) (int, int, error) {
	log.Printf("🔄 Starting Batch Auto Order for date: %s", date)

	entries, err := b.fetchAllBPJSEntries(date)
	if err != nil {
		return 0, 0, err
	}

	log.Printf("📋 Found %d BPJS patients", len(entries))

	successCount := 0
	for idx, entry := range entries {
		startTime := time.Now()

		// Extract date for display
		tanggal := entry.TanggalPeriksa
		if len(tanggal) >= 10 {
			tanggal = tanggal[:10]
		}

		log.Printf("[%d/%d] %s - %s | %s | %s", idx+1, len(entries), entry.NoRkmMedis, entry.NamaPasien, entry.NamaPoli, tanggal)

		// Get task times
		tasks, generated, err := b.fetchTaskTimes(entry)
		if err != nil {
			log.Printf("   ❌ Error: %v", err)
			continue
		}

		// Apply auto order
		orderedTasks := b.processor.ProcessTasks(tasks)

		// Check if any tasks available
		hasAnyTask := false
		for i := 0; i < 7; i++ {
			if orderedTasks[i] != nil {
				hasAnyTask = true
				break
			}
		}

		if !hasAnyTask {
			log.Printf("   ⚠️ Skip - no task times")
			continue
		}

		// Show compact task changes (only tasks that exist)
		for i := 0; i < 7; i++ {
			if orderedTasks[i] != nil {
				origTime := ""
				newTime := orderedTasks[i].Format("15:04:05")
				if tasks[i] != nil {
					origTime = tasks[i].Format("15:04:05")
				}
				if origTime != newTime {
					log.Printf("   Task %d: %s → %s", i+1, origTime, newTime)
				} else {
					log.Printf("   Task %d: %s", i+1, newTime)
				}
			}
		}

		// Save to database
		if err := b.saveTaskIDs(entry, orderedTasks, generated); err != nil {
			log.Printf("   ❌ Error saving: %v", err)
			continue
		}

		// Save report if autoorder only
		result := models.ProcessResult{
			NomorReferensi: entry.NomorReferensi,
			KodeBooking:    entry.KodeBooking,
			NoRkmMedis:     entry.NoRkmMedis,
			NamaPasien:     entry.NamaPasien,
			NoRawat:        entry.NoRawat,
			ProcessedAt:    time.Now(),
			Tasks:          make(map[int]models.TaskResult),
			AutoOrderDone:  true,
		}

		for i := 0; i < 7; i++ {
			if orderedTasks[i] != nil {
				result.Tasks[i+1] = models.TaskResult{
					Waktu: orderedTasks[i].Format("2006-01-02 15:04:05"),
				}
			}
		}

		b.reportStore.SaveResult(result)

		elapsed := time.Since(startTime)
		log.Printf("   ✓ Done in %.1fs", elapsed.Seconds())
		successCount++
	}

	log.Printf("✅ Batch Auto Order complete: %d/%d success", successCount, len(entries))
	return len(entries), successCount, nil
}

// BatchUpdateWaktu sends Update Waktu to BPJS for all processed entries on a date
func (b *BatchHandler) BatchUpdateWaktu(date string) (int, int, error) {
	log.Printf("🔄 Starting Batch Update Waktu for date: %s", date)

	entries, err := b.fetchEntriesWithTaskIDs(date)
	if err != nil {
		return 0, 0, err
	}

	log.Printf("📋 Found %d entries with Task IDs", len(entries))

		successCount := 0
	for _, entry := range entries {
		log.Printf("   Sending: %s - %s", entry.NoRkmMedis, entry.NamaPasien)

		result := models.ProcessResult{
			NomorReferensi: entry.NomorReferensi,
			KodeBooking:    entry.KodeBooking,
			NoRkmMedis:     entry.NoRkmMedis,
			NamaPasien:     entry.NamaPasien,
			NoRawat:        entry.NoRawat,
			ProcessedAt:    time.Now(),
			Tasks:          make(map[int]models.TaskResult),
			AutoOrderDone:  true,
		}

		tasks, generated, err := b.fetchTaskTimes(entry)
		if err != nil {
			log.Printf("   ❌ Error getting task times: %v", err)
			continue
		}
		ordered := b.processor.ProcessTasks(tasks)
		if err := b.saveTaskIDs(entry, ordered, generated); err != nil {
			log.Printf("   ❌ Error saving normalized tasks: %v", err)
		}

		allSuccess := true
		lastAcceptedMs := b.getMaxSentTime(entry.NomorReferensi)
		for i := 0; i < 7; i++ {
			taskNum := i + 1
			if ordered[i] == nil {
				continue
			}
			waktuMs := TimeToMillis(ordered[i])
			if lastAcceptedMs > 0 && waktuMs <= lastAcceptedMs {
				waktuMs = lastAcceptedMs + 60_000
				b.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMs)
			}

			resp, err := b.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMs)
			taskResult := models.TaskResult{
				Waktu: time.UnixMilli(waktuMs).Format("2006-01-02 15:04:05"),
			}

			if err != nil {
				taskResult.BPJSStatus = "error"
				taskResult.Message = err.Error()
				allSuccess = false
				log.Printf("   ├── Task %d: ❌ Error", taskNum)
			} else {
				taskResult.BPJSCode = resp.Metadata.Code
				if resp.IsSuccess() {
					taskResult.BPJSStatus = "success"
					log.Printf("   ├── Task %d: ✓ 200 OK", taskNum)
					b.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
					lastAcceptedMs = waktuMs
				} else {
					msgLower := strings.ToLower(resp.Metadata.Message)
					if strings.Contains(msgLower, "tidak boleh kurang atau sama") {
						delta := int64(3_600_000)
						waktuMsRetry := maxInt64(waktuMs, lastAcceptedMs) + delta
						nextMinMs := int64(0)
						for k := i + 1; k < 7; k++ {
							if ordered[k] != nil {
								m := TimeToMillis(ordered[k])
								if nextMinMs == 0 || m < nextMinMs {
									nextMinMs = m
								}
							}
						}
						if nextMinMs > 0 && waktuMsRetry >= nextMinMs {
							ordered = b.adjustForward(entry, ordered, i, waktuMsRetry)
						}
						resp2, err2 := b.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMsRetry)
						if err2 == nil && resp2.IsSuccess() {
							taskResult.BPJSCode = resp2.Metadata.Code
							taskResult.BPJSStatus = "success"
							taskResult.Message = ""
							taskResult.Waktu = time.UnixMilli(waktuMsRetry).Format("2006-01-02 15:04:05")
							b.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMsRetry)
							b.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
							log.Printf("   ├── Task %d: ✓ 200 OK (retry +1h)", taskNum)
							lastAcceptedMs = waktuMsRetry
						} else {
							taskResult.BPJSStatus = "failed"
							taskResult.Message = resp.Metadata.Message
							allSuccess = false
							log.Printf("   ├── Task %d: %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
						}
					} else {
						taskResult.BPJSStatus = "failed"
						taskResult.Message = resp.Metadata.Message
						allSuccess = false
						log.Printf("   ├── Task %d: %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
					}
				}
			}
			result.Tasks[taskNum] = taskResult
		}

		result.UpdateWaktuDone = allSuccess
		b.reportStore.SaveResult(result)

		if allSuccess {
			successCount++
		}
	}

	log.Printf("✅ Batch Update Waktu complete: %d/%d success", successCount, len(entries))
	return len(entries), successCount, nil
}

// BatchAll runs both auto order and update waktu per-patient (atomic processing)
func (b *BatchHandler) BatchAll(date string) (int, int, error) {
	log.Printf("🔄 Starting Batch Auto Order + Update Waktu for date: %s", date)

	entries, err := b.fetchAllBPJSEntries(date)
	if err != nil {
		return 0, 0, err
	}

	log.Printf("📋 Found %d BPJS patients", len(entries))

	successCount := 0
	for idx, entry := range entries {
		startTime := time.Now()

		// Extract date for display
		tanggal := entry.TanggalPeriksa
		if len(tanggal) >= 10 {
			tanggal = tanggal[:10]
		}

		log.Printf("[%d/%d] %s - %s | %s | %s", idx+1, len(entries), entry.NoRkmMedis, entry.NamaPasien, entry.NamaPoli, tanggal)

		// Step 1: Get task times
		tasks, generated, err := b.fetchTaskTimes(entry)
		if err != nil {
			log.Printf("   ❌ Error: %v", err)
			continue
		}

		// Step 2: Apply auto order
		orderedTasks := b.processor.ProcessTasks(tasks)

		// Check if any tasks available
		hasAnyTask := false
		for i := 0; i < 7; i++ {
			if orderedTasks[i] != nil {
				hasAnyTask = true
				break
			}
		}

		if !hasAnyTask {
			log.Printf("   ⚠️ Skip - no task times")
			continue
		}

		// Show compact task changes
		for i := 0; i < 7; i++ {
			if orderedTasks[i] != nil {
				origTime := ""
				newTime := orderedTasks[i].Format("15:04:05")
				if tasks[i] != nil {
					origTime = tasks[i].Format("15:04:05")
				}
				if origTime != newTime {
					log.Printf("   Task %d: %s → %s", i+1, origTime, newTime)
				} else {
					log.Printf("   Task %d: %s", i+1, newTime)
				}
			}
		}

		// Step 3: Save to database
		if err := b.saveTaskIDs(entry, orderedTasks, generated); err != nil {
			log.Printf("   ❌ Error saving: %v", err)
			continue
		}

		result := models.ProcessResult{
			NomorReferensi: entry.NomorReferensi,
			KodeBooking:    entry.KodeBooking,
			NoRkmMedis:     entry.NoRkmMedis,
			NamaPasien:     entry.NamaPasien,
			NoRawat:        entry.NoRawat,
			ProcessedAt:    time.Now(),
			Tasks:          make(map[int]models.TaskResult),
			AutoOrderDone:  true,
		}

		// Step 4: Send to BPJS API
		allSuccess := true
		for i := 0; i < 7; i++ {
			taskNum := i + 1
			if orderedTasks[i] == nil {
				continue
			}

			waktuMs := TimeToMillis(orderedTasks[i])
			resp, err := b.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMs)

			taskResult := models.TaskResult{
				Waktu: orderedTasks[i].Format("2006-01-02 15:04:05"),
			}

			if err != nil {
				taskResult.BPJSStatus = "error"
				taskResult.Message = err.Error()
				log.Printf("   BPJS T%d: ❌ Error: %v", taskNum, err)
				allSuccess = false
			} else if resp.IsSuccess() {
				taskResult.BPJSCode = resp.Metadata.Code
				taskResult.BPJSStatus = "success"
				log.Printf("   BPJS T%d: ✓ %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
				b.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
			} else {
				taskResult.BPJSCode = resp.Metadata.Code
				taskResult.BPJSStatus = "failed"
				taskResult.Message = resp.Metadata.Message
				log.Printf("   BPJS T%d: ✗ %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
				allSuccess = false
			}
			result.Tasks[taskNum] = taskResult
		}

		result.UpdateWaktuDone = allSuccess
		b.reportStore.SaveResult(result)

		elapsed := time.Since(startTime)
		log.Printf("   ✓ Done in %.1fs", elapsed.Seconds())

		if allSuccess {
			successCount++
		}
	}

	log.Printf("✅ Complete: %d/%d success", successCount, len(entries))
	return len(entries), successCount, nil
}

// fetchAllBPJSEntries gets all BPJS patients for a date
func (b *BatchHandler) fetchAllBPJSEntries(date string) ([]models.AntrianReferensi, error) {
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
			COALESCE(pj.png_jawab, '') as png_jawab,
			COALESCE(pol.nm_poli, '') as nm_poli
		FROM mlite_antrian_referensi mar
		LEFT JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		LEFT JOIN pasien p ON mar.no_rkm_medis = p.no_rkm_medis
		LEFT JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		LEFT JOIN poliklinik pol ON rp.kd_poli = pol.kd_poli
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
		ORDER BY rp.jam_reg ASC
	`
	return b.executeQuery(query, date)
}

// fetchEntriesWithTaskIDs gets entries that have task IDs saved
func (b *BatchHandler) fetchEntriesWithTaskIDs(date string) ([]models.AntrianReferensi, error) {
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
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
			AND EXISTS (SELECT 1 FROM mlite_antrian_referensi_taskid t WHERE t.nomor_referensi = mar.nomor_referensi)
		ORDER BY rp.jam_reg ASC
	`
	return b.executeQuery(query, date)
}

func (b *BatchHandler) executeQuery(query string, date string) ([]models.AntrianReferensi, error) {
	rows, err := b.db.DB.Query(query, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.AntrianReferensi
	for rows.Next() {
		var e models.AntrianReferensi
		if err := rows.Scan(
			&e.TanggalPeriksa, &e.NoRkmMedis, &e.NomorKartu, &e.NomorReferensi,
			&e.KodeBooking, &e.JenisKunjungan, &e.StatusKirim, &e.Keterangan,
			&e.NamaPasien, &e.NoRawat, &e.PngJawab, &e.NamaPoli,
		); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (b *BatchHandler) getTaskIDsFromDB(nomorReferensi string) (map[int]int64, error) {
	rows, err := b.db.DB.Query(`
		SELECT taskid, waktu FROM mlite_antrian_referensi_taskid 
		WHERE nomor_referensi = ?
	`, nomorReferensi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int]int64)
	for rows.Next() {
		var taskID int
		var waktu int64
		if err := rows.Scan(&taskID, &waktu); err == nil {
			result[taskID] = waktu
		}
	}
	return result, nil
}

// Reuse methods from watcher - simplified versions
func (b *BatchHandler) fetchTaskTimes(entry models.AntrianReferensi) ([7]*time.Time, [7]bool, error) {
	w := &Watcher{db: b.db, processor: NewAutoOrderProcessor()}
	return w.fetchTaskTimes(entry)
}

func (b *BatchHandler) saveTaskIDs(entry models.AntrianReferensi, tasks [7]*time.Time, generated [7]bool) error {
	// Extract date part from TanggalPeriksa
	tanggal := entry.TanggalPeriksa
	if len(tanggal) >= 10 {
		tanggal = tanggal[:10]
	}

	_, err := b.db.DB.Exec("DELETE FROM mlite_antrian_referensi_taskid WHERE nomor_referensi = ?", entry.NomorReferensi)
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

	for i := 0; i < 7; i++ {
		if tasks[i] == nil {
			continue
		}
		waktuMs := TimeToMillis(tasks[i])
		ket := keterangan[i]
		if generated[i] {
			ket = ket + " [generated]"
		}
		_, err := b.db.DB.Exec(`
			INSERT INTO mlite_antrian_referensi_taskid 
			(tanggal_periksa, nomor_referensi, taskid, waktu, status, keterangan)
			VALUES (?, ?, ?, ?, 'Belum', ?)
		`, tanggal, entry.NomorReferensi, i+1, waktuMs, ket)
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *BatchHandler) updateTaskStatus(nomorReferensi string, taskID int, status string) {
	_, _ = b.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid SET status = ? WHERE nomor_referensi = ? AND taskid = ?
	`, status, nomorReferensi, taskID)
}

func (b *BatchHandler) updateTaskWaktu(nomorReferensi string, taskID int, waktuMs int64) {
	_, _ = b.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid SET waktu = ? WHERE nomor_referensi = ? AND taskid = ? AND status != 'Sudah'
	`, waktuMs, nomorReferensi, taskID)
}

func (b *BatchHandler) getMaxSentTime(nomorReferensi string) int64 {
	var maxWaktu sql.NullInt64
	_ = b.db.DB.QueryRow(`
		SELECT COALESCE(MAX(waktu), 0) FROM mlite_antrian_referensi_taskid 
		WHERE nomor_referensi = ? AND status = 'Sudah'
	`, nomorReferensi).Scan(&maxWaktu)
	if maxWaktu.Valid {
		return maxWaktu.Int64
	}
	return 0
}

func (b *BatchHandler) fetchEntryByNomorReferensi(nr string) (*models.AntrianReferensi, error) {
	row := b.db.DB.QueryRow(`
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
			COALESCE(pj.png_jawab, '') as png_jawab,
			COALESCE(pol.nm_poli, '') as nm_poli
		FROM mlite_antrian_referensi mar
		LEFT JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		LEFT JOIN pasien p ON mar.no_rkm_medis = p.no_rkm_medis
		LEFT JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		LEFT JOIN poliklinik pol ON rp.kd_poli = pol.kd_poli
		WHERE mar.nomor_referensi = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
	`, nr)
	var e models.AntrianReferensi
	if err := row.Scan(
		&e.TanggalPeriksa, &e.NoRkmMedis, &e.NomorKartu, &e.NomorReferensi,
		&e.KodeBooking, &e.JenisKunjungan, &e.StatusKirim, &e.Keterangan,
		&e.NamaPasien, &e.NoRawat, &e.PngJawab, &e.NamaPoli,
	); err != nil {
		return nil, err
	}
	return &e, nil
}

func (b *BatchHandler) fetchEntriesFailedTask3ByReport(date string) ([]models.AntrianReferensi, error) {
	results, err := b.reportStore.GetResultsByDate(date)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var entries []models.AntrianReferensi
	for _, r := range results {
		t3, ok := r.Tasks[3]
		if ok {
			if strings.ToLower(t3.BPJSStatus) == "failed" || strings.ToLower(t3.BPJSStatus) == "error" {
				if !seen[r.NomorReferensi] {
					if e, err := b.fetchEntryByNomorReferensi(r.NomorReferensi); err == nil {
						entries = append(entries, *e)
						seen[r.NomorReferensi] = true
					}
				}
			}
		}
	}
	return entries, nil
}

// fetchEntriesFailedTask3 gets entries where Task 3 is not 'Sudah'
func (b *BatchHandler) fetchEntriesFailedTask3(date string) ([]models.AntrianReferensi, error) {
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
			COALESCE(pj.png_jawab, '') as png_jawab,
			COALESCE(pol.nm_poli, '') as nm_poli
		FROM mlite_antrian_referensi mar
		LEFT JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		LEFT JOIN pasien p ON mar.no_rkm_medis = p.no_rkm_medis
		LEFT JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		LEFT JOIN poliklinik pol ON rp.kd_poli = pol.kd_poli
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
			AND EXISTS (
				SELECT 1 FROM mlite_antrian_referensi_taskid t 
				WHERE t.nomor_referensi = mar.nomor_referensi 
				AND t.taskid = 3 
				AND t.status != 'Sudah'
			)
		ORDER BY rp.jam_reg ASC
	`
	return b.executeQuery(query, date)
}

// BatchRetryTask3 reprocesses and resubmits Task 3 for failed entries
func (b *BatchHandler) BatchRetryTask3(date string) (int, int, error) {
	log.Printf("🔄 Starting Batch Retry Task 3 for date: %s", date)
	entries, err := b.fetchEntriesFailedTask3ByReport(date)
	if err != nil {
		return 0, 0, err
	}
	if len(entries) == 0 {
		fallbackEntries, err2 := b.fetchEntriesFailedTask3(date)
		if err2 == nil {
			entries = fallbackEntries
		}
	}
	log.Printf("📋 Found %d entries to retry Task 3", len(entries))

	successCount := 0
	for idx, entry := range entries {
		startTime := time.Now()
		tanggal := entry.TanggalPeriksa
		if len(tanggal) >= 10 {
			tanggal = tanggal[:10]
		}
		log.Printf("[%d/%d] %s - %s | %s | %s", idx+1, len(entries), entry.NoRkmMedis, entry.NamaPasien, entry.NamaPoli, tanggal)

		// Auto order ulang
		tasks, generated, err := b.fetchTaskTimes(entry)
		if err != nil {
			log.Printf("   ❌ Error get tasks: %v", err)
			continue
		}
		ordered := b.processor.ProcessTasks(tasks)
		if err := b.saveTaskIDs(entry, ordered, generated); err != nil {
			log.Printf("   ❌ Error save tasks: %v", err)
			continue
		}

		// Kirim ulang hanya Task 3
		i := 2
		taskNum := 3
		if ordered[i] == nil {
			log.Printf("   ⚠️ Skip - Task 3 kosong")
			continue
		}
		lastAcceptedMs := b.getMaxSentTime(entry.NomorReferensi)
		waktuMs := TimeToMillis(ordered[i])
		if lastAcceptedMs > 0 && waktuMs <= lastAcceptedMs {
			waktuMs = lastAcceptedMs + 60_000
			b.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMs)
		}

		resp, err := b.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMs)
		result := models.ProcessResult{
			NomorReferensi: entry.NomorReferensi,
			KodeBooking:    entry.KodeBooking,
			NoRkmMedis:     entry.NoRkmMedis,
			NamaPasien:     entry.NamaPasien,
			NoRawat:        entry.NoRawat,
			ProcessedAt:    time.Now(),
			Tasks:          make(map[int]models.TaskResult),
			AutoOrderDone:  true,
		}
		taskResult := models.TaskResult{
			Waktu: time.UnixMilli(waktuMs).Format("2006-01-02 15:04:05"),
		}

		if err != nil {
			taskResult.BPJSStatus = "error"
			taskResult.Message = err.Error()
			log.Printf("   ├── Task 3: ❌ Error: %v", err)
		} else {
			taskResult.BPJSCode = resp.Metadata.Code
			msgLower := strings.ToLower(resp.Metadata.Message)
			if resp.IsSuccess() {
				taskResult.BPJSStatus = "success"
				log.Printf("   ├── Task 3: ✓ 200 OK")
				b.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
				successCount++
				result.UpdateWaktuDone = true
			} else if strings.Contains(msgLower, "tidak boleh kurang atau sama") {
				delta := int64(3_600_000)
				waktuMsRetry := maxInt64(waktuMs, lastAcceptedMs) + delta
				nextMinMs := int64(0)
				for k := i + 1; k < 7; k++ {
					if ordered[k] != nil {
						m := TimeToMillis(ordered[k])
						if nextMinMs == 0 || m < nextMinMs {
							nextMinMs = m
						}
					}
				}
				if nextMinMs > 0 && waktuMsRetry >= nextMinMs {
					ordered = b.adjustForward(entry, ordered, i, waktuMsRetry)
				}
				resp2, err2 := b.bpjsClient.UpdateWaktu(entry.KodeBooking, taskNum, waktuMsRetry)
				if err2 == nil && resp2.IsSuccess() {
					taskResult.BPJSCode = resp2.Metadata.Code
					taskResult.BPJSStatus = "success"
					taskResult.Message = ""
					taskResult.Waktu = time.UnixMilli(waktuMsRetry).Format("2006-01-02 15:04:05")
					b.updateTaskWaktu(entry.NomorReferensi, taskNum, waktuMsRetry)
					b.updateTaskStatus(entry.NomorReferensi, taskNum, "Sudah")
					log.Printf("   ├── Task 3: ✓ 200 OK (retry +1h)")
					successCount++
					result.UpdateWaktuDone = true
				} else {
					taskResult.BPJSStatus = "failed"
					taskResult.Message = resp.Metadata.Message
					log.Printf("   ├── Task 3: %d %s", resp.Metadata.Code, resp.Metadata.Message)
					result.UpdateWaktuDone = false
				}
			} else {
				taskResult.BPJSStatus = "failed"
				taskResult.Message = resp.Metadata.Message
				log.Printf("   ├── Task 3: %d %s", resp.Metadata.Code, resp.Metadata.Message)
				result.UpdateWaktuDone = false
			}
		}

		result.Tasks[taskNum] = taskResult
		b.reportStore.SaveResult(result)
		elapsed := time.Since(startTime)
		log.Printf("   ✓ Done in %.1fs", elapsed.Seconds())
	}

	log.Printf("✅ Retry Task 3 complete: %d/%d success", successCount, len(entries))
	return len(entries), successCount, nil
}
func (b *BatchHandler) adjustForward(entry models.AntrianReferensi, ordered [7]*time.Time, startIdx int, baseMs int64) [7]*time.Time {
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
				b.updateTaskWaktu(entry.NomorReferensi, k+1, newT.UnixMilli())
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
