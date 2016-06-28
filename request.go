package grab

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
	"net/http"
	"net/url"
)

// A Request represents an HTTP file transfer request to be sent by a Client.
type Request struct {
	// Label is an arbitrary string which may used to label a Request with a
	// user friendly name.
	Label string

	// Tag is an arbitrary interface which may be used to relate a Request to
	// other data.
	Tag interface{}

	// HTTPRequest specifies the http.Request to be sent to the remote server to
	// initiate a file transfer. It includes request configuration such as URL,
	// protocol version, HTTP method, request headers and authentication.
	HTTPRequest *http.Request

	// Filename specifies the path where the file transfer will be stored in
	// local storage.
	//
	// An empty string means the transfer will be stored in the current working
	// directory.
	Filename string

	// Size specifies the expected size of the file transfer if known. If the
	// server response size does not match, the transfer is cancelled and an
	// error returned.
	Size uint64

	// BufferSize specifies the size in bytes of the buffer that is used for
	// transferring the requested file. Larger buffers may result in faster
	// throughput but will use more memory and result in less frequent updates
	// to the transfer progress statistics. Default: 4096.
	BufferSize uint

	// Hash specifies the hashing algorithm that will be used to compute the
	// checksum value of the transferred file.
	//
	// If Checksum or Hash is nil, no checksum validation occurs.
	Hash hash.Hash

	// Checksum specifies the expected checksum value of the transferred file.
	//
	// If Checksum or Hash is nil, no checksum validation occurs.
	Checksum []byte

	// RemoveOnError specifies that any completed download should be deleted if
	// it fails checksum validation.
	RemoveOnError bool

	// NotifyOnClose specifies a channel that will notified when the requested
	// transfer is completed, either successfully or with an error.
	NotifyOnClose chan<- *Response

	// notifyOnCloseInternal is the same as NotifyOnClose but for private
	// internal use.
	notifyOnCloseInternal chan *Response
}

// NewRequest returns a new file transfer Request suitable for use with
// Client.Do.
func NewRequest(urlStr string) (*Request, error) {
	// create http request
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	return &Request{
		HTTPRequest: req,
	}, nil
}

// URL returns the URL to be requested from the remote server.
func (c *Request) URL() *url.URL {
	return c.HTTPRequest.URL
}

// SetChecksum sets the expected checksum value and hashing algorithm to use
// when validating a completed file transfer.
//
// The following hashing algorithms are supported:
//	md5
//	sha1
//	sha256
//	sha512
func (c *Request) SetChecksum(algorithm string, checksum []byte) error {
	switch algorithm {
	case "md5":
		c.Hash = md5.New()
	case "sha1":
		c.Hash = sha1.New()
	case "sha256":
		c.Hash = sha256.New()
	case "sha512":
		c.Hash = sha512.New()
	default:
		return fmt.Errorf("Unsupported hashing algorithm: %s", algorithm)
	}

	c.Checksum = checksum

	return nil
}
