package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gotrol/internal/models"
)

// DailySummary holds pre-calculated summary for a single day
type DailySummary struct {
	Processed int
	Success   int
	Failed    int
}

// Store handles report storage in JSON files with in-memory cache
type Store struct {
	basePath string
	mu       sync.RWMutex
	// In-memory cache: date -> summary
	cache   map[string]*DailySummary
	cacheMu sync.RWMutex
}

type DailyData struct {
	Date    string                 `json:"date"`
	Results []models.ProcessResult `json:"results"`
}

func NewStore(dbPath string) (*Store, error) {
	// Create directory if not exists
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	// Use the path as base directory for JSON files
	basePath := filepath.Dir(dbPath)
	if basePath == "." {
		basePath = "./reports"
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, err
	}

	store := &Store{
		basePath: basePath,
		cache:    make(map[string]*DailySummary),
	}

	// Pre-load cache from existing files (last 31 days)
	store.preloadCache()

	return store, nil
}

// preloadCache loads summary data from existing JSON files into memory
func (s *Store) preloadCache() {
	today := time.Now()
	for i := 0; i < 31; i++ {
		date := today.AddDate(0, 0, -i).Format("2006-01-02")
		s.loadDaySummaryToCache(date)
	}
}

// loadDaySummaryToCache reads a day's file and caches the summary
func (s *Store) loadDaySummaryToCache(date string) {
	filePath := s.getFilePath(date)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return // File doesn't exist, skip
	}

	var daily DailyData
	if err := json.Unmarshal(data, &daily); err != nil {
		return
	}

	summary := &DailySummary{}
	for _, r := range daily.Results {
		summary.Processed++
		if isSuccessResult(r) {
			summary.Success++
		} else {
			summary.Failed++
		}
	}

	s.cacheMu.Lock()
	s.cache[date] = summary
	s.cacheMu.Unlock()
}

// updateCache updates the in-memory cache for a specific date
func (s *Store) updateCache(date string, result models.ProcessResult, isNew bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if s.cache[date] == nil {
		s.cache[date] = &DailySummary{}
	}

	if isNew {
		s.cache[date].Processed++
	}

	// Recalculate success/failed based on current state
	// For simplicity, we just reload from the cached results
	// A more optimized approach would track deltas
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

// SaveResult saves a process result to the JSON file and updates cache
func (s *Store) SaveResult(result models.ProcessResult) error {
	date := result.ProcessedAt.Format("2006-01-02")

	daily, err := s.loadDailyData(date)
	if err != nil {
		daily = &DailyData{Date: date, Results: []models.ProcessResult{}}
	}

	// Check if already exists
	for i, r := range daily.Results {
		if r.NomorReferensi == result.NomorReferensi {
			daily.Results[i] = result
			if err := s.saveDailyData(daily); err != nil {
				return err
			}
			s.refreshCacheForDate(date, daily)
			return nil
		}
	}

	daily.Results = append(daily.Results, result)
	if err := s.saveDailyData(daily); err != nil {
		return err
	}
	s.refreshCacheForDate(date, daily)
	return nil
}

// isSuccessResult checks if a result should be counted as success
// BPJSCode 200 = success, 208 = already sent (also success)
func isSuccessResult(r models.ProcessResult) bool {
	if r.UpdateWaktuDone {
		return true
	}
	// Check if any task has BPJSCode 200 or 208
	for _, task := range r.Tasks {
		if task.BPJSCode == 200 || task.BPJSCode == 208 {
			return true
		}
	}
	return false
}

// refreshCacheForDate recalculates and updates the cache for a specific date
func (s *Store) refreshCacheForDate(date string, daily *DailyData) {
	summary := &DailySummary{}
	for _, r := range daily.Results {
		summary.Processed++
		if isSuccessResult(r) {
			summary.Success++
		} else {
			summary.Failed++
		}
	}

	s.cacheMu.Lock()
	s.cache[date] = summary
	s.cacheMu.Unlock()
}

// GetResultsByDate gets all results for a specific date
func (s *Store) GetResultsByDate(date string) ([]models.ProcessResult, error) {
	daily, err := s.loadDailyData(date)
	if err != nil {
		return nil, err
	}
	return daily.Results, nil
}

// GetSummaryByDate gets summary statistics for a date
func (s *Store) GetSummaryByDate(date string) (processed, success, failed int, err error) {
	results, err := s.GetResultsByDate(date)
	if err != nil {
		return 0, 0, 0, err
	}

	for _, r := range results {
		processed++
		if isSuccessResult(r) {
			success++
		} else {
			failed++
		}
	}
	return
}

// GetSummaryByDateRange gets summary for a date range - uses in-memory cache for speed
func (s *Store) GetSummaryByDateRange(startDate, endDate string) (processed, success, failed int, err error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return 0, 0, 0, err
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return 0, 0, 0, err
	}

	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		if summary, ok := s.cache[dateStr]; ok {
			processed += summary.Processed
			success += summary.Success
			failed += summary.Failed
		}
	}
	return
}

// IsProcessed checks if a nomor_referensi has been processed today
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
