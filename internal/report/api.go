package report

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gotrol/internal/database"
	"gotrol/internal/models"
)

// APIServer handles HTTP API for dashboard
type APIServer struct {
	store  *Store
	db     *database.MySQL
	port   int
	server *http.Server
}

func NewAPIServer(store *Store, db *database.MySQL, port int) *APIServer {
	return &APIServer{
		store: store,
		db:    db,
		port:  port,
	}
}

// Start starts the API server
func (a *APIServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/reports/today", a.handleReportsToday)
	mux.HandleFunc("/api/reports", a.handleReports)
	mux.HandleFunc("/api/reports/summary", a.handleReportsSummary)
	mux.HandleFunc("/api/stats/overview", a.handleStatsOverview)
	mux.HandleFunc("/api/patients/monthly", a.handlePatientsMonthly)
	mux.HandleFunc("/api/patients/registration", a.handlePatientsRegistration)

	// Serve static files (UI)
	// Make sure to use absolute path or correct relative path depending on execution context
	fs := http.FileServer(http.Dir("web"))
	mux.Handle("/", fs)

	a.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", a.port),
		Handler: mux,
	}

	log.Printf("✓ Report API started at http://localhost:%d", a.port)
	return a.server.ListenAndServe()
}

// Stop stops the API server
func (a *APIServer) Stop() error {
	if a.server != nil {
		return a.server.Close()
	}
	return nil
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "running",
		"version": "1.0.0",
		"time":    time.Now().Format(time.RFC3339),
	})
}

func (a *APIServer) handleReportsToday(w http.ResponseWriter, r *http.Request) {
	today := time.Now().Format("2006-01-02")
	a.getReportByDate(w, r, today)
}

func (a *APIServer) handleReports(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	a.getReportByDate(w, r, date)
}

func (a *APIServer) getReportByDate(w http.ResponseWriter, r *http.Request, date string) {
	w.Header().Set("Content-Type", "application/json")

	// Parse pagination params
	page := 1
	limit := 10
	search := r.URL.Query().Get("search")

	if p := r.URL.Query().Get("page"); p != "" {
		if val, err := strconv.Atoi(p); err == nil && val > 0 {
			page = val
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 && val <= 100 {
			limit = val
		}
	}

	// Get total BPJS patients for the date
	totalBPJS := a.getTotalBPJSPatients(date)

	// Get all results
	allResults, err := a.store.GetResultsByDate(date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply search filter
	var filteredResults []models.ProcessResult
	if search != "" {
		searchLower := strings.ToLower(search)
		for _, r := range allResults {
			if strings.Contains(strings.ToLower(r.NamaPasien), searchLower) ||
				strings.Contains(strings.ToLower(r.NoRkmMedis), searchLower) ||
				strings.Contains(strings.ToLower(r.KodeBooking), searchLower) {
				filteredResults = append(filteredResults, r)
			}
		}
	} else {
		filteredResults = allResults
	}

	// Calculate pagination
	totalFiltered := len(filteredResults)
	totalPages := (totalFiltered + limit - 1) / limit
	if totalPages == 0 {
		totalPages = 1
	}

	// Slice for current page
	start := (page - 1) * limit
	end := start + limit
	if start > totalFiltered {
		start = totalFiltered
	}
	if end > totalFiltered {
		end = totalFiltered
	}

	paginatedResults := filteredResults[start:end]

	processed, success, failed, _ := a.store.GetSummaryByDate(date)

	// Extended response with pagination info
	response := map[string]interface{}{
		"date":                date,
		"total_bpjs_patients": totalBPJS,
		"total_processed":     processed,
		"total_success_sent":  success,
		"total_failed":        failed,
		"total_pending":       totalBPJS - processed,
		"items":               paginatedResults,
		"pagination": map[string]interface{}{
			"page":        page,
			"limit":       limit,
			"total_items": totalFiltered,
			"total_pages": totalPages,
		},
	}

	json.NewEncoder(w).Encode(response)
}

func (a *APIServer) handleReportsSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	today := time.Now()
	todayStr := today.Format("2006-01-02")

	// Calculate start of week (Monday)
	weekday := int(today.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekStart := today.AddDate(0, 0, -(weekday - 1))

	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())

	var (
		wg sync.WaitGroup
		// Today
		todayBPJS      int
		todayProcessed int
		todaySuccess   int
		todayFailed    int
		// Week
		weekBPJS      int
		weekProcessed int
		weekSuccess   int
		weekFailed    int
		// Month
		monthBPJS      int
		monthProcessed int
		monthSuccess   int
		monthFailed    int
	)

	wg.Add(3)

	// Fetch Today Stats
	go func() {
		defer wg.Done()
		todayBPJS = a.getTotalBPJSPatients(todayStr)
		todayProcessed, todaySuccess, todayFailed, _ = a.store.GetSummaryByDate(todayStr)
	}()

	// Fetch Week Stats
	go func() {
		defer wg.Done()
		start := weekStart.Format("2006-01-02")
		log.Printf("DEBUG: Week Range: %s to %s", start, todayStr)
		weekBPJS = a.getTotalBPJSPatientsRange(start, todayStr)
		weekProcessed, weekSuccess, weekFailed, _ = a.store.GetSummaryByDateRange(start, todayStr)
		log.Printf("DEBUG: Week Stats: BPJS=%d, Proc=%d, Succ=%d", weekBPJS, weekProcessed, weekSuccess)
	}()

	// Fetch Month Stats
	go func() {
		defer wg.Done()
		start := monthStart.Format("2006-01-02")
		log.Printf("DEBUG: Month Range: %s to %s", start, todayStr)
		monthBPJS = a.getTotalBPJSPatientsRange(start, todayStr)
		monthProcessed, monthSuccess, monthFailed, _ = a.store.GetSummaryByDateRange(start, todayStr)
		log.Printf("DEBUG: Month Stats: BPJS=%d, Proc=%d, Succ=%d", monthBPJS, monthProcessed, monthSuccess)
	}()

	wg.Wait()

	summary := map[string]models.ReportSummary{
		"today": {
			TotalBPJSPatients: todayBPJS,
			TotalProcessed:    todayProcessed,
			TotalSuccessSent:  todaySuccess,
			TotalFailed:       todayFailed,
			TotalPending:      todayBPJS - todayProcessed,
		},
		"this_week": {
			TotalBPJSPatients: weekBPJS,
			TotalProcessed:    weekProcessed,
			TotalSuccessSent:  weekSuccess,
			TotalFailed:       weekFailed,
			TotalPending:      weekBPJS - weekProcessed,
		},
		"this_month": {
			TotalBPJSPatients: monthBPJS,
			TotalProcessed:    monthProcessed,
			TotalSuccessSent:  monthSuccess,
			TotalFailed:       monthFailed,
			TotalPending:      monthBPJS - monthProcessed,
		},
	}

	json.NewEncoder(w).Encode(summary)
}

// getTotalBPJSPatients counts BPJS patients for a date from MySQL
func (a *APIServer) getTotalBPJSPatients(date string) int {
	var count int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
	`, date).Scan(&count)
	return count
}

// getTotalBPJSPatientsRange counts BPJS patients for a date range
func (a *APIServer) getTotalBPJSPatientsRange(startDate, endDate string) int {
	var count int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa BETWEEN ? AND ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
	`, startDate, endDate).Scan(&count)
	return count
}

// handleStatsOverview returns comprehensive statistics comparing BPJS status with GoTrol processing
func (a *APIServer) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	// Query MySQL for BPJS patient status breakdown
	var totalBPJS, statusSudah, statusBelum, taskSent int

	// Total BPJS patients for the date
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
AND DATE(rp.tgl_registrasi) = mar.tanggal_periksa
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
	`, date).Scan(&totalBPJS)

	// Status "Sudah" - already sent via old system
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
AND DATE(rp.tgl_registrasi) = mar.tanggal_periksa
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
			AND mar.status_kirim = 'Sudah'
	`, date).Scan(&statusSudah)

	// Status "Belum" - not yet sent
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
AND DATE(rp.tgl_registrasi) = mar.tanggal_periksa
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND rp.kd_pj = 'BPJ'
			AND (mar.status_kirim = 'Belum' OR mar.status_kirim IS NULL OR mar.status_kirim = '')
	`, date).Scan(&statusBelum)

	// Count TaskID sent via GoTrol (Task 5 with status Sudah means complete)
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT t.nomor_referensi)
		FROM mlite_antrian_referensi_taskid t
		WHERE t.tanggal_periksa = ?
			AND t.taskid = 5
			AND t.status = 'Sudah'
	`, date).Scan(&taskSent)

	// Get GoTrol processed count from report store
	gotrolProcessed, gotrolSuccess, gotrolFailed, _ := a.store.GetSummaryByDate(date)

	response := map[string]interface{}{
		"date": date,
		"bpjs_patients": map[string]interface{}{
			"total":        totalBPJS,
			"status_sudah": statusSudah,
			"status_belum": statusBelum,
		},
		"task_id": map[string]interface{}{
			"total_sent": taskSent,
			"pending":    totalBPJS - taskSent,
			"success_rate": func() float64 {
				if totalBPJS == 0 {
					return 0
				}
				return float64(taskSent) / float64(totalBPJS) * 100
			}(),
		},
		"gotrol": map[string]interface{}{
			"processed": gotrolProcessed,
			"success":   gotrolSuccess,
			"failed":    gotrolFailed,
		},
	}

	json.NewEncoder(w).Encode(response)
}

// handlePatientsMonthly returns monthly BPJS patient data directly from MySQL
func (a *APIServer) handlePatientsMonthly(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse year and month from query params (default: current month)
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if y := r.URL.Query().Get("year"); y != "" {
		if val, err := strconv.Atoi(y); err == nil && val >= 2020 && val <= 2100 {
			year = val
		}
	}
	if m := r.URL.Query().Get("month"); m != "" {
		if val, err := strconv.Atoi(m); err == nil && val >= 1 && val <= 12 {
			month = val
		}
	}

	// Calculate date range for the month
	startDate := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	endDate := startDate.AddDate(0, 1, -1) // Last day of month

	startStr := startDate.Format("2006-01-02")
	endStr := endDate.Format("2006-01-02")

	log.Printf("DEBUG Monthly API: Querying %s to %s", startStr, endStr)

	// Fast query: Total patients - NO JOIN, query directly from mlite_antrian_referensi
	// All records with kodebooking are BPJS patients
	var totalPatients int
	err := a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT nomor_referensi)
		FROM mlite_antrian_referensi
		WHERE tanggal_periksa BETWEEN ? AND ?
			AND kodebooking != ''
	`, startStr, endStr).Scan(&totalPatients)
	if err != nil {
		log.Printf("DEBUG Monthly API - Total error: %v", err)
	}

	// Count status Sudah - NO JOIN
	var statusSudah int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT nomor_referensi)
		FROM mlite_antrian_referensi
		WHERE tanggal_periksa BETWEEN ? AND ?
			AND kodebooking != ''
			AND status_kirim = 'Sudah'
	`, startStr, endStr).Scan(&statusSudah)

	// Count status Belum - NO JOIN
	var statusBelum int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT nomor_referensi)
		FROM mlite_antrian_referensi
		WHERE tanggal_periksa BETWEEN ? AND ?
			AND kodebooking != ''
			AND (status_kirim = 'Belum' OR status_kirim IS NULL OR status_kirim = '')
	`, startStr, endStr).Scan(&statusBelum)

	log.Printf("DEBUG Monthly API Result: Total=%d, Sudah=%d, Belum=%d", totalPatients, statusSudah, statusBelum)

	response := map[string]interface{}{
		"year":       year,
		"month":      month,
		"month_name": time.Month(month).String(),
		"period": map[string]string{
			"start": startStr,
			"end":   endStr,
		},
		"total_patients": totalPatients,
		"status": map[string]interface{}{
			"sudah": statusSudah,
			"belum": statusBelum,
		},
	}

	json.NewEncoder(w).Encode(response)
}

// handlePatientsRegistration returns patient registration data with referensi and task timeline
func (a *APIServer) handlePatientsRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get date from query param (default: today)
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	// Pagination params
	page := 1
	limit := 10
	if p := r.URL.Query().Get("page"); p != "" {
		if pInt, err := strconv.Atoi(p); err == nil && pInt > 0 {
			page = pInt
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if lInt, err := strconv.Atoi(l); err == nil && lInt > 0 && lInt <= 100 {
			limit = lInt
		}
	}

	// Search query
	searchQuery := strings.TrimSpace(r.URL.Query().Get("search"))

	log.Printf("DEBUG Registration API: date=%s, page=%d, limit=%d, search=%s", date, page, limit, searchQuery)

	// Build the base query with optional search filter
	baseQuery := `
		FROM reg_periksa 
		INNER JOIN pasien ON reg_periksa.no_rkm_medis = pasien.no_rkm_medis 
		INNER JOIN dokter ON reg_periksa.kd_dokter = dokter.kd_dokter 
		INNER JOIN poliklinik ON reg_periksa.kd_poli = poliklinik.kd_poli 
		INNER JOIN penjab ON reg_periksa.kd_pj = penjab.kd_pj 
		LEFT JOIN mlite_antrian_referensi mar ON mar.no_rkm_medis = pasien.no_rkm_medis 
			AND mar.tanggal_periksa = reg_periksa.tgl_registrasi
		WHERE reg_periksa.tgl_registrasi = ?
			AND reg_periksa.kd_pj = 'BPJ'
	`

	args := []interface{}{date}

	if searchQuery != "" {
		baseQuery += ` AND (pasien.nm_pasien LIKE ? OR pasien.no_rkm_medis LIKE ? OR COALESCE(mar.nomor_referensi, '') LIKE ?)`
		searchPattern := "%" + searchQuery + "%"
		args = append(args, searchPattern, searchPattern, searchPattern)
	}

	// Count total for pagination
	countQuery := "SELECT COUNT(*) " + baseQuery
	var totalItems int
	if err := a.db.DB.QueryRow(countQuery, args...).Scan(&totalItems); err != nil {
		log.Printf("ERROR Registration count query: %v", err)
		totalItems = 0
	}

	// Main query - get patient registration with joins
	query := `
		SELECT 
			pasien.no_peserta,
			pasien.no_rkm_medis,
			pasien.nm_pasien,
			reg_periksa.no_rawat,
			reg_periksa.tgl_registrasi,
			reg_periksa.jam_reg,
			poliklinik.nm_poli,
			dokter.nm_dokter,
			penjab.png_jawab,
			COALESCE(mar.nomor_referensi, '') as nomor_referensi,
			COALESCE(mar.kodebooking, '') as kodebooking,
			COALESCE(mar.status_kirim, '') as status_kirim
	` + baseQuery + `
		ORDER BY reg_periksa.jam_reg ASC
		LIMIT ? OFFSET ?
	`

	offset := (page - 1) * limit
	paginatedArgs := append(args, limit, offset)

	rows, err := a.db.DB.Query(query, paginatedArgs...)
	if err != nil {
		log.Printf("ERROR Registration query: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type TaskTime struct {
		TaskID int    `json:"task_id"`
		Waktu  string `json:"waktu"`
	}

	type PatientReg struct {
		NoPeserta      string     `json:"no_peserta"`
		NoRKMMedis     string     `json:"no_rkm_medis"`
		NamaPasien     string     `json:"nama_pasien"`
		NoRawat        string     `json:"no_rawat"`
		TglRegistrasi  string     `json:"tgl_registrasi"`
		JamReg         string     `json:"jam_reg"`
		NamaPoli       string     `json:"nama_poli"`
		NamaDokter     string     `json:"nama_dokter"`
		Penjamin       string     `json:"penjamin"`
		NomorReferensi string     `json:"nomor_referensi"`
		KodeBooking    string     `json:"kodebooking"`
		StatusKirim    string     `json:"status_kirim"`
		Tasks          []TaskTime `json:"tasks"`
	}

	var patients []PatientReg

	for rows.Next() {
		var p PatientReg
		var jamReg []byte
		err := rows.Scan(
			&p.NoPeserta, &p.NoRKMMedis, &p.NamaPasien, &p.NoRawat,
			&p.TglRegistrasi, &jamReg, &p.NamaPoli, &p.NamaDokter,
			&p.Penjamin, &p.NomorReferensi, &p.KodeBooking, &p.StatusKirim,
		)
		if err != nil {
			log.Printf("ERROR scan: %v", err)
			continue
		}
		p.JamReg = string(jamReg)

		// Get task times for this patient
		if p.NomorReferensi != "" {
			taskRows, err := a.db.DB.Query(`
				SELECT taskid, waktu 
				FROM mlite_antrian_referensi_taskid 
				WHERE nomor_referensi = ? 
				ORDER BY taskid
			`, p.NomorReferensi)
			if err == nil {
				defer taskRows.Close()
				for taskRows.Next() {
					var taskID int
					var waktuMs int64
					if err := taskRows.Scan(&taskID, &waktuMs); err == nil {
						// Convert ms timestamp to datetime string
						waktuStr := ""
						if waktuMs > 0 {
							t := time.Unix(waktuMs/1000, (waktuMs%1000)*1000000)
							waktuStr = t.Format("15:04:05")
						}
						p.Tasks = append(p.Tasks, TaskTime{TaskID: taskID, Waktu: waktuStr})
					}
				}
			}
		}

		patients = append(patients, p)
	}

	log.Printf("DEBUG Registration Result: total=%d patients (page %d)", len(patients), page)

	// Calculate total pages
	totalPages := (totalItems + limit - 1) / limit
	if totalPages == 0 {
		totalPages = 1
	}

	response := map[string]interface{}{
		"date":     date,
		"total":    totalItems,
		"patients": patients,
		"pagination": map[string]interface{}{
			"page":        page,
			"limit":       limit,
			"total_items": totalItems,
			"total_pages": totalPages,
		},
	}

	json.NewEncoder(w).Encode(response)
}
