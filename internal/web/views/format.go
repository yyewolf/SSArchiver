package views

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func Number[T ~int | ~int64](n T) string {
	s := strconv.FormatInt(int64(n), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func Percent(f float64) string { return fmt.Sprintf("%.2f%%", f*100) }

func PP(f float64) string {
	if f <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fpp", f)
}

func DifficultyName(d int) string {
	switch d {
	case 1:
		return "Easy"
	case 3:
		return "Normal"
	case 5:
		return "Hard"
	case 7:
		return "Expert"
	case 9:
		return "Expert+"
	}
	return "Unknown"
}

func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(math.Max(0, d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func TimeAgo(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 10*time.Second {
		return "just now"
	}
	if d > 30*24*time.Hour {
		return t.Format("2 Jan 2006")
	}
	return HumanDuration(d) + " ago"
}

func Until(t, now time.Time) string {
	if !t.After(now) {
		return "due"
	}
	return "in " + HumanDuration(t.Sub(now))
}

func HumanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func Initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		r := []rune(w)
		out = append(out, unicode.ToUpper(r[0]))
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}
