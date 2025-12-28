package models

import "time"

// AntrianReferensi represents mlite_antrian_referensi table
type AntrianReferensi struct {
	TanggalPeriksa  string
	NoRkmMedis      string
	NomorKartu      string
	NomorReferensi  string
	KodeBooking     string
	JenisKunjungan  string
	StatusKirim     string
	Keterangan      string
	NamaPasien      string
	NoRawat         string
	PngJawab        string // Jenis Bayar
	NamaPoli        string // Nama Poliklinik
}

// TaskID represents mlite_antrian_referensi_taskid table
type TaskID struct {
	TanggalPeriksa string
	NomorReferensi string
	TaskID         int
	Waktu          int64  // Unix timestamp in milliseconds
	Status         string
	Keterangan     string
}

// TaskIDSet holds all 7 task IDs for a patient
type TaskIDSet struct {
	Task1 *time.Time
	Task2 *time.Time
	Task3 *time.Time
	Task4 *time.Time
	Task5 *time.Time
	Task6 *time.Time
	Task7 *time.Time
}

// ProcessResult represents the result of processing one patient
type ProcessResult struct {
	NomorReferensi   string
	KodeBooking      string
	NoRkmMedis       string
	NamaPasien       string
	NoRawat          string
	AutoOrderDone    bool
	UpdateWaktuDone  bool
	ProcessedAt      time.Time
	Tasks            map[int]TaskResult
	Error            string
}

// TaskResult represents BPJS API response for each task
type TaskResult struct {
	Waktu      string
	BPJSStatus string
	BPJSCode   int
	Message    string
}

// ReportSummary for dashboard API
type ReportSummary struct {
	TotalBPJSPatients int `json:"total_bpjs_patients"`
	TotalProcessed    int `json:"total_processed"`
	TotalSuccessSent  int `json:"total_success_sent"`
	TotalFailed       int `json:"total_failed"`
	TotalPending      int `json:"total_pending"`
}

// DailyReport for dashboard API
type DailyReport struct {
	Date              string          `json:"date"`
	TotalBPJSPatients int             `json:"total_bpjs_patients"`
	TotalProcessed    int             `json:"total_processed"`
	TotalSuccessSent  int             `json:"total_success_sent"`
	TotalFailed       int             `json:"total_failed"`
	TotalPending      int             `json:"total_pending"`
	Items             []ProcessResult `json:"items"`
}
