// pkg/plugin/grpc/fs.go
//
// Filesystem abstraction for artifact streaming.
// openFile is a variable so tests can inject a fake reader without a real file.

package grpc

import "os"

// openFile opens a file for reading. Override in tests to inject fake readers.
var openFile = func(path string) (interface {
	Read([]byte) (int, error)
	Close() error
}, error) {
	return os.Open(path)
}
