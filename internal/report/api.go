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
			AND pj.png_jawab LIKE '%BPJS%'
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
			AND pj.png_jawab LIKE '%BPJS%'
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
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
	`, date).Scan(&totalBPJS)

	// Status "Sudah" - already sent via old system
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
			AND mar.status_kirim = 'Sudah'
	`, date).Scan(&statusSudah)

	// Status "Belum" - not yet sent
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa = ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
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

	// Query: Total BPJS patients for the month
	var totalPatients int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa BETWEEN ? AND ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
	`, startStr, endStr).Scan(&totalPatients)

	// Query: Daily breakdown
	type DailyCount struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	}
	var dailyBreakdown []DailyCount

	rows, err := a.db.DB.Query(`
		SELECT mar.tanggal_periksa, COUNT(DISTINCT mar.nomor_referensi) as cnt
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa BETWEEN ? AND ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
		GROUP BY mar.tanggal_periksa
		ORDER BY mar.tanggal_periksa
	`, startStr, endStr)

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var dc DailyCount
			if err := rows.Scan(&dc.Date, &dc.Count); err == nil {
				dailyBreakdown = append(dailyBreakdown, dc)
			}
		}
	}

	// Query: Status counts (Sudah vs Belum)
	var statusSudah, statusBelum int
	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa BETWEEN ? AND ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
			AND mar.status_kirim = 'Sudah'
	`, startStr, endStr).Scan(&statusSudah)

	a.db.DB.QueryRow(`
		SELECT COUNT(DISTINCT mar.nomor_referensi)
		FROM mlite_antrian_referensi mar
		JOIN reg_periksa rp ON mar.no_rkm_medis = rp.no_rkm_medis 
			AND mar.tanggal_periksa = rp.tgl_registrasi
		JOIN penjab pj ON rp.kd_pj = pj.kd_pj
		WHERE mar.tanggal_periksa BETWEEN ? AND ?
			AND mar.kodebooking != ''
			AND pj.png_jawab LIKE '%BPJS%'
			AND (mar.status_kirim = 'Belum' OR mar.status_kirim IS NULL OR mar.status_kirim = '')
	`, startStr, endStr).Scan(&statusBelum)

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
		"daily_breakdown": dailyBreakdown,
	}

	json.NewEncoder(w).Encode(response)
}
