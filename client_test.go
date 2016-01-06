package grab

import (
	"testing"
	"time"
)

func TestClient_do(t *testing.T) {
	url := "http://mirror.centos.org/centos/7/updates/x86_64/repodata/3a2896e638c89f478598fab313a444b84146f363d275ae7b7330fc8998246b2f-filelists.sqlite.bz2"

	// create client and request
	client := NewClient()
	req, err := NewRequest(url)
	if err != nil {
		t.Fatalf("Error initializing download: %v", err)
	}

	// fetch asyncronously
	resp, err := client.DoAsync(req)
	if err != nil {
		t.Fatalf("Error with download: %v", err)
	}

	// gauge progress every 100ms
	timer := time.NewTicker(100 * time.Millisecond)
	for now := range timer.C {
		t.Logf("%v %d%% ( %d / %d )\n", now, int(resp.Progress()*100), resp.BytesTransferred(), resp.Size)

		if resp.IsComplete() {
			timer.Stop()
			break
		}
	}

	t.Logf("Download completed in %v", resp.End.Sub(resp.Start))
}
