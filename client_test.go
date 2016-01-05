package grab

import (
	"testing"
)

func TestClient_do(t *testing.T) {
	url := "http://mirror.centos.org/centos/7/updates/x86_64/repodata/3a2896e638c89f478598fab313a444b84146f363d275ae7b7330fc8998246b2f-filelists.sqlite.bz2"

	client := NewClient("grab test")

	d, err := NewDownload(".", url, 0, nil, nil)
	if err != nil {
		t.Fatalf("Error initializing download: %v", err)
	}

	if err := client.Do(d); err != nil {
		t.Fatalf("Error with download: %v", err)
	}
}
