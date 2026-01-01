package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gotrol/internal/models"
)

type Store struct {
	basePath string
	mu       sync.RWMutex
}

type DailyData struct {
	Date    string                 `json:"date"`
	Results []models.ProcessResult `json:"results"`
}

func NewStore(dbPath string) (*Store, error) {

	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	basePath := filepath.Dir(dbPath)
	if basePath == "." {
		basePath = "./reports"
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, err
	}

	return &Store{basePath: basePath}, nil
}

func (s *Store) Close() error {
	return nil
}

func (s *Store) getFilePath(date string) string {
	return filepath.Join(s.basePath, date+".json")
}

func (s *Store) loadDailyData(date string) (*DailyData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := s.getFilePath(date)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &DailyData{Date: date, Results: []models.ProcessResult{}}, nil
		}
		return nil, err
	}

	var daily DailyData
	if err := json.Unmarshal(data, &daily); err != nil {
		return &DailyData{Date: date, Results: []models.ProcessResult{}}, nil
	}
	return &daily, nil
}

func (s *Store) saveDailyData(data *DailyData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.getFilePath(data.Date)
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, jsonData, 0644)
}

func (s *Store) SaveResult(result models.ProcessResult) error {
	date := result.ProcessedAt.Format("2006-01-02")

	daily, err := s.loadDailyData(date)
	if err != nil {
		daily = &DailyData{Date: date, Results: []models.ProcessResult{}}
	}

	for i, r := range daily.Results {
		if r.NomorReferensi == result.NomorReferensi {
			daily.Results[i] = result
			return s.saveDailyData(daily)
		}
	}

	daily.Results = append(daily.Results, result)
	return s.saveDailyData(daily)
}

func (s *Store) GetResultsByDate(date string) ([]models.ProcessResult, error) {
	daily, err := s.loadDailyData(date)
	if err != nil {
		return nil, err
	}
	return daily.Results, nil
}

func (s *Store) GetSummaryByDate(date string) (processed, success, failed int, err error) {
	results, err := s.GetResultsByDate(date)
	if err != nil {
		return 0, 0, 0, err
	}

	for _, r := range results {
		processed++
		if r.UpdateWaktuDone {
			success++
		} else {
			failed++
		}
	}
	return
}

func (s *Store) GetSummaryByDateRange(startDate, endDate string) (processed, success, failed int, err error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return 0, 0, 0, err
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return 0, 0, 0, err
	}

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		p, s, f, err := s.GetSummaryByDate(dateStr)
		if err == nil {
			if p > 0 {
			}
			processed += p
			success += s
			failed += f
		}
	}
	return
}

func (s *Store) IsProcessed(nomorReferensi string, date string) bool {
	results, err := s.GetResultsByDate(date)
	if err != nil {
		return false
	}

	for _, r := range results {
		if r.NomorReferensi == nomorReferensi && r.UpdateWaktuDone {
			return true
		}
	}
	return false
}
