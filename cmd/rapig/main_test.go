package main

import "testing"

func TestSplitStorageMigrateFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantFrom string
		wantTo   string
		wantDry  bool
		wantRest []string
		wantErr  bool
	}{
		{
			name:     "space separated values",
			args:     []string{"migrate", "-from", "2222", "-to", "local", "-dry-run", "-data", "D:/x"},
			wantFrom: "2222",
			wantTo:   "local",
			wantDry:  true,
			wantRest: []string{"-data", "D:/x"},
		},
		{
			name:     "equals form",
			args:     []string{"migrate", "--from=2222", "-to=local", "-data", "D:/x"},
			wantFrom: "2222",
			wantTo:   "local",
			wantRest: []string{"-data", "D:/x"},
		},
		{
			name:    "missing value for -from",
			args:    []string{"migrate", "-from"},
			wantErr: true,
		},
		{
			name:    "missing -to",
			args:    []string{"migrate", "-from", "2222"},
			wantErr: true,
		},
		{
			name:    "not migrate",
			args:    []string{"list"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, opts, err := splitStorageMigrateFlags(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.from != tt.wantFrom || opts.to != tt.wantTo || opts.dryRun != tt.wantDry {
				t.Fatalf("opts = %+v, want from=%q to=%q dry=%v", opts, tt.wantFrom, tt.wantTo, tt.wantDry)
			}
			if len(rest) != len(tt.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tt.wantRest)
			}
			for i := range rest {
				if rest[i] != tt.wantRest[i] {
					t.Fatalf("rest = %v, want %v", rest, tt.wantRest)
				}
			}
		})
	}
}
