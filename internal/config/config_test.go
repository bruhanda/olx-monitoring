package config

import (
	"strings"
	"testing"
)

func TestParseNotifyTimes(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"11:00,15:00,20:00", "11:00,15:00,20:00", false},
		{" 20:00 , 09:05 ", "09:05,20:00", false},   // сортується
		{"11:00, 11:00", "11:00", false},            // дублікати прибираються
		{"9:5", "09:05", false},                     // нормалізується
		{"", "", false},                             // порожньо = режим інтервалу
		{"25:00", "", true},
		{"о десятій", "", true},
		{"11-00", "", true},
	}
	for _, tc := range cases {
		got, err := ParseNotifyTimes(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseNotifyTimes(%q) — очікувалась помилка, отримано %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseNotifyTimes(%q) = помилка %v", tc.in, err)
			continue
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("ParseNotifyTimes(%q) = %v, want %q", tc.in, got, tc.want)
		}
	}
}
