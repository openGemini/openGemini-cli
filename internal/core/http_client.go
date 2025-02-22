// Copyright 2025 openGemini Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/openGemini/opengemini-client-go/opengemini"
)

var _ HttpClient = (*HttpClientCreator)(nil)

type HttpClient interface {
	Ping() (time.Duration, string, error)
	QueryContext(context.Context, opengemini.Query) (*opengemini.QueryResult, error)
	Write(database, retentionPolicy string, point *opengemini.Point) error
	Close()
}

type HttpClientCreator struct {
	client *http.Client
}

func NewHttpClient(cfg *CommandLineConfig) (HttpClient, error) {
	var client = &HttpClientCreator{client: &http.Client{
		Timeout: time.Duration(cfg.Timeout) * time.Millisecond,
	}}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if cfg.EnableTls {
		certificateManager, err := NewCertificateManager(cfg.CACert, cfg.Cert, cfg.CertKey)
		if err != nil {
			return nil, errors.New("cannot load certificate: " + err.Error())
		}
		transport.TLSClientConfig = &tls.Config{
			RootCAs:      certificateManager.CAPool,
			Certificates: []tls.Certificate{certificateManager.Certificate},
		}

		if cfg.InsecureTls {
			transport.TLSClientConfig.InsecureSkipVerify = true
		}

		if cfg.InsecureHostname {
			transport.TLSClientConfig.InsecureSkipVerify = true
			transport.TLSClientConfig.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("no certificates provided by server")
				}
				cert, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return fmt.Errorf("failed to parse server certificate: %w", err)
				}
				now := time.Now()
				if now.After(cert.NotAfter) {
					return errors.New("server certificate has expired")
				}
				opts := x509.VerifyOptions{
					DNSName:       "",
					Roots:         certificateManager.CAPool,
					Intermediates: x509.NewCertPool(),
				}
				for _, rawCert := range rawCerts[1:] {
					intermediateCert, err := x509.ParseCertificate(rawCert)
					if err != nil {
						return fmt.Errorf("failed to parse intermediate certificate: %w", err)
					}
					opts.Intermediates.AddCert(intermediateCert)
				}
				if _, err := cert.Verify(opts); err != nil {
					return fmt.Errorf("server certificate chain validation failed: %w", err)
				}
				return nil
			}
		}
	}

	if cfg.UnixSocket != "" {
		transport.DisableCompression = true
		transport.DialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", cfg.UnixSocket)
		}
	}

	return client, nil
}

func (h *HttpClientCreator) Ping() (time.Duration, string, error) {
	//TODO implement me
	panic("implement me")
}

func (h *HttpClientCreator) QueryContext(ctx context.Context, query opengemini.Query) (*opengemini.QueryResult, error) {
	//TODO implement me
	panic("implement me")
}

func (h *HttpClientCreator) Write(database, retentionPolicy string, point *opengemini.Point) error {
	//TODO implement me
	panic("implement me")
}

func (h *HttpClientCreator) Close() {
	//TODO implement me
	panic("implement me")
}

type CertificateManager struct {
	CAContent   []byte
	CAPool      *x509.CertPool
	Certificate tls.Certificate
}

func NewCertificateManager(ca, certificate, certificateKey string) (*CertificateManager, error) {
	var cm = new(CertificateManager)
	if ca != "" {
		content, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		cm.CAPool = x509.NewCertPool()
		if !cm.CAPool.AppendCertsFromPEM(content) {
			return nil, errors.New("failed to parse ca certificate")
		}
	}
	if certificate != "" && certificateKey != "" {
		keyPair, err := tls.LoadX509KeyPair(certificate, certificateKey)
		if err != nil {
			return nil, err
		}
		cm.Certificate = keyPair
	}

	return cm, nil
}
