package main

import "testing"

func TestWorkspaceRelativePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "workspace config",
			path: "/workspace/provisioners/step-0/config.json",
			want: "provisioners/step-0/config.json",
		},
		{
			name: "workspace root file",
			path: "/workspace/status.json",
			want: "status.json",
		},
		{
			name:    "relative path",
			path:    "provisioners/step-0/config.json",
			wantErr: true,
		},
		{
			name:    "path traversal",
			path:    "/workspace/../etc/passwd",
			wantErr: true,
		},
		{
			name:    "outside workspace",
			path:    "/tmp/config.json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := workspaceRelativePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("workspaceRelativePath(%q) returned nil error", tt.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("workspaceRelativePath(%q) returned error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("workspaceRelativePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
