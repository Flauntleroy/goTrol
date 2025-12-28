package service

import (
	"math/rand"
	"time"
)

// AutoOrderProcessor handles the auto ordering logic for Task IDs
type AutoOrderProcessor struct{}

func NewAutoOrderProcessor() *AutoOrderProcessor {
	return &AutoOrderProcessor{}
}

// ProcessTasks applies auto order logic to the task times
// This is ported from the JavaScript autoOrderAndSaveTaskId function
func (p *AutoOrderProcessor) ProcessTasks(tasks [7]*time.Time) [7]*time.Time {
	result := tasks

	// Step 1: If task 6 and 7 are the same (and not nil), clear both
	if result[5] != nil && result[6] != nil {
		if result[5].Equal(*result[6]) {
			result[5] = nil
			result[6] = nil
		}
	}

	// Step 2: Set minimum time to 08:00
	for i := 0; i < 7; i++ {
		if result[i] != nil {
			t := *result[i]
			if t.Hour() < 8 {
				t = time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, t.Location())
				result[i] = &t
			}
		}
	}

	// Step 3: Special handling for Task 4 - must be greater than Task 3
	if result[2] != nil && result[3] != nil {
		task3 := *result[2]
		task4 := *result[3]
		if task4.Before(task3) || task4.Equal(task3) {
			// Add 1-5 random minutes from task 3
			randomMinutes := rand.Intn(5) + 1
			newTask4 := task3.Add(time.Duration(randomMinutes) * time.Minute)

			// If task 5 exists, make sure task 4 doesn't exceed it
			if result[4] != nil {
				task5 := *result[4]
				if newTask4.After(task5) || newTask4.Equal(task5) {
					maxAllowed := int(task5.Sub(task3).Minutes()) - 1
					if maxAllowed < 1 {
						maxAllowed = 1
					}
					newTask4 = task3.Add(time.Duration(maxAllowed) * time.Minute)
				}
			}
			result[3] = &newTask4
		}
	}

	// Step 4: Ensure sequential order - each task must be after the previous
	for i := 1; i < 7; i++ {
		if result[i-1] != nil && result[i] != nil {
			prev := *result[i-1]
			curr := *result[i]
			if curr.Before(prev) || curr.Equal(prev) {
				// Add 1-5 random minutes from previous task
				randomMinutes := rand.Intn(5) + 1
				newTime := prev.Add(time.Duration(randomMinutes) * time.Minute)

				// If next task exists, make sure we don't exceed it
				if i+1 < 7 && result[i+1] != nil {
					nextTask := *result[i+1]
					if newTime.After(nextTask) || newTime.Equal(nextTask) {
						maxAllowed := int(nextTask.Sub(prev).Minutes()) - 1
						if maxAllowed < 1 {
							maxAllowed = 1
						}
						newTime = prev.Add(time.Duration(maxAllowed) * time.Minute)
					}
				}
				result[i] = &newTime
			}
		}
	}

	// Step 5: Validate task 6 & 7 - if either is empty, clear both
	if result[5] == nil || result[6] == nil {
		result[5] = nil
		result[6] = nil
	}

	// Step 6: If task 6 or 7 equals task 1 or 2, clear both 6 and 7
	if result[5] != nil && result[6] != nil {
		task1 := result[0]
		task2 := result[1]
		task6 := result[5]
		task7 := result[6]

		shouldClear := false
		if task1 != nil && (task6.Equal(*task1) || task7.Equal(*task1)) {
			shouldClear = true
		}
		if task2 != nil && (task6.Equal(*task2) || task7.Equal(*task2)) {
			shouldClear = true
		}
		if shouldClear {
			result[5] = nil
			result[6] = nil
		}
	}

	return result
}

// TimeToMillis converts time.Time to Unix milliseconds
func TimeToMillis(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

// MillisToTime converts Unix milliseconds to time.Time
func MillisToTime(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms)
	return &t
}

// FormatTime formats time for display
func FormatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
