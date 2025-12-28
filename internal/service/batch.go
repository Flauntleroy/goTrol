package service

import (
	"log"
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
		tasks, err := b.fetchTaskTimes(entry)
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
		if err := b.saveTaskIDs(entry, orderedTasks); err != nil {
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

		// Get task IDs from database
		taskIDs, err := b.getTaskIDsFromDB(entry.NomorReferensi)
		if err != nil {
			log.Printf("   ❌ Error getting task IDs: %v", err)
			continue
		}

		allSuccess := true
		for taskNum, waktuMs := range taskIDs {
			if waktuMs == 0 {
				continue
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
				} else {
					taskResult.BPJSStatus = "failed"
					taskResult.Message = resp.Metadata.Message
					allSuccess = false
					log.Printf("   ├── Task %d: %d %s", taskNum, resp.Metadata.Code, resp.Metadata.Message)
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
		tasks, err := b.fetchTaskTimes(entry)
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
		if err := b.saveTaskIDs(entry, orderedTasks); err != nil {
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
			AND pj.png_jawab LIKE '%BPJS%'
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
			AND pj.png_jawab LIKE '%BPJS%'
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
func (b *BatchHandler) fetchTaskTimes(entry models.AntrianReferensi) ([7]*time.Time, error) {
	// Same logic as watcher.fetchTaskTimes
	watcher := &Watcher{db: b.db}
	return watcher.getTaskTimesFromSources(entry)
}

func (b *BatchHandler) saveTaskIDs(entry models.AntrianReferensi, tasks [7]*time.Time) error {
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
		_, err := b.db.DB.Exec(`
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

func (b *BatchHandler) updateTaskStatus(nomorReferensi string, taskID int, status string) {
	_, _ = b.db.DB.Exec(`
		UPDATE mlite_antrian_referensi_taskid SET status = ? WHERE nomor_referensi = ? AND taskid = ?
	`, status, nomorReferensi, taskID)
}
