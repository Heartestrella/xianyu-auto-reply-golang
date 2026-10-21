package scheduler

import (
	"fmt"
	"time"
)

func ValidatePolishWindow(start, end string) error {
	a, err := time.Parse("15:04", start)
	if err != nil || a.Format("15:04") != start {
		return fmt.Errorf("开始时间必须为 HH:MM")
	}
	b, err := time.Parse("15:04", end)
	if err != nil || b.Format("15:04") != end {
		return fmt.Errorf("结束时间必须为 HH:MM")
	}
	if a.After(b) {
		return fmt.Errorf("结束时间不能早于开始时间，请设置同一天内的时段")
	}
	return nil
}

func PolishWindow(cfg map[string]any) (string, string) {
	start, end := "00:00", "23:59"
	if v, ok := cfg["run_start_time"].(string); ok {
		start = v
	}
	if v, ok := cfg["run_end_time"].(string); ok {
		end = v
	}
	return start, end
}

func canRunAutomatically(code string, cfg map[string]any, now time.Time) bool {
	if code != "polish" {
		return true
	}
	start, end := PolishWindow(cfg)
	if ValidatePolishWindow(start, end) != nil {
		return false
	}
	clock := now.In(time.Local).Format("15:04")
	return clock >= start && clock <= end
}
