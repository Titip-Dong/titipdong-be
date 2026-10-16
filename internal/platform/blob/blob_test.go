package blob

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

const azuriteConnectionString = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
	"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
	"BlobEndpoint=http://azurite:10000/devstoreaccount1;"

func TestSignedURLs(t *testing.T) {
	s, err := New(Config{ConnectionString: azuriteConnectionString, Container: "uploads", PublicURL: "http://localhost:10000"})
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2026, 10, 16, 9, 28, 30, 0, time.UTC)
	raw, err := s.UploadURL("order_photo/01a143fc-e824-744b-9e33-f87c49d01ede.jpg", expiry)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "localhost:10000" || !strings.HasPrefix(u.Path, "/devstoreaccount1/uploads/order_photo/") {
		t.Errorf("URL = %s, want the public host and the blob path", raw)
	}
	q := u.Query()
	if q.Get("sp") != "cw" || q.Get("sr") != "b" || q.Get("se") != "2026-10-16T09:28:30Z" {
		t.Errorf("query = %v", q)
	}
	// Signature Azurite computes for this blob, permissions, and expiry with its development key.
	if got, want := q.Get("sig"), "uCkZaEARYWxyj3UjeMi129Hy7+sT1f7mt6E3S3PVEt0="; got != want {
		t.Errorf("sig = %s, want %s", got, want)
	}

	read, err := s.ReadURL("order_photo/x.png", expiry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "sp=r&") {
		t.Errorf("read URL = %s, want read-only permission", read)
	}
}

func TestNewRequiresAccountKey(t *testing.T) {
	_, err := New(Config{ConnectionString: "BlobEndpoint=http://azurite:10000/devstoreaccount1;SharedAccessSignature=sv=x", Container: "uploads"})
	if err == nil {
		t.Fatal("New accepted a connection string without an account key")
	}
}
