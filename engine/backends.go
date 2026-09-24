package engine

// What a program needs to open a database without importing the core
// (DESIGN D6): a key and a backend. Other backends (several mount points,
// S3, a root kept apart) are the core's, configured by the host.

// NewKeyring makes a fresh master key. (Stub.)
func NewKeyring() (*Keyring, error) { return nil, ErrInternal }

// MemoryBlobs is a backend in memory: a database on it lives as long as
// the process. (Stub.)
func MemoryBlobs() Blobs { return nil }

// DiskBlobs is a backend in the directory dir, which it creates if need
// be. (Stub.)
func DiskBlobs(dir string) (Blobs, error) { return nil, ErrInternal }

// AllowAll is an authorizer that allows every call: for a program with no
// principals to tell apart. A database shared by people takes Grants.
func AllowAll() Authorizer { return nil }

// ParseDocument parses JSON text into a document with the document model's
// bounded parser; what is not a document is ErrInvalid. (Stub.)
func ParseDocument(text []byte) (Node, error) { return Node{}, ErrInvalid }
