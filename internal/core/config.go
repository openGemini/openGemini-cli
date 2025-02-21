package core

type CommandLineConfig struct {
	Host       string
	Port       int
	UnixSocket string
	Username   string
	Password   string
	Database   string
	Type       string
	Precision  string

	EnableTls        bool
	InsecureTls      bool
	CACert           string
	Cert             string
	CertKey          string
	InsecureHostname bool
}
