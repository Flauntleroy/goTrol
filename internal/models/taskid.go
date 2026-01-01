package models

import "time"

type AntrianReferensi struct {
	TanggalPeriksa string
	NoRkmMedis     string
	NomorKartu     string
	NomorReferensi string
	KodeBooking    string
	JenisKunjungan string
	StatusKirim    string
	Keterangan     string
	NamaPasien     string
	NoRawat        string
	PngJawab       string
	NamaPoli       string
}

type TaskID struct {
	TanggalPeriksa string
	NomorReferensi string
	TaskID         int
	Waktu          int64
	Status         string
	Keterangan     string
}

type TaskIDSet struct {
	Task1 *time.Time
	Task2 *time.Time
	Task3 *time.Time
	Task4 *time.Time
	Task5 *time.Time
	Task6 *time.Time
	Task7 *time.Time
}

type ProcessResult struct {
	NomorReferensi  string
	KodeBooking     string
	NoRkmMedis      string
	NamaPasien      string
	NoRawat         string
	AutoOrderDone   bool
	UpdateWaktuDone bool
	ProcessedAt     time.Time
	Tasks           map[int]TaskResult
	Error           string
}

type TaskResult struct {
	Waktu      string
	BPJSStatus string
	BPJSCode   int
	Message    string
}

type ReportSummary struct {
	TotalBPJSPatients int `json:"total_bpjs_patients"`
	TotalProcessed    int `json:"total_processed"`
	TotalSuccessSent  int `json:"total_success_sent"`
	TotalFailed       int `json:"total_failed"`
	TotalPending      int `json:"total_pending"`
}

type DailyReport struct {
	Date              string          `json:"date"`
	TotalBPJSPatients int             `json:"total_bpjs_patients"`
	TotalProcessed    int             `json:"total_processed"`
	TotalSuccessSent  int             `json:"total_success_sent"`
	TotalFailed       int             `json:"total_failed"`
	TotalPending      int             `json:"total_pending"`
	Items             []ProcessResult `json:"items"`
}
