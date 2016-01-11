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

// A Request represents an HTTP file transfer request to be sent by a client.
type Request struct {
	// Label is an arbitrary string which may used to identify a request when it
	// is returned, attached to a Response.
	Label string

	// Tag is an arbitrary interface which may be used to relate a request to
	// other data when it is returned, attached to a Response.
	Tag interface{}

	// HTTPRequest specifies the HTTP request to be sent to the remote server
	// to initiate a file transfer. It includes request configuration such as
	// URL, protocol version, HTTP method, request headers and authentication.
	HTTPRequest *http.Request

	// Filename specifies the path where the file transfer will be stored in
	// local storage.
	//
	// If the given Filename is a directory, the file transfer will be stored in
	// that directory and the file's name will be determined using
	// Content-Disposition headers in the server's response or from the base
	// name in the request URL.
	//
	// An empty string means the transfer will be stored in the current working
	// directory.
	Filename string

	// RemoveOnError specifies that any completed download should be deleted if
	// it fails checksum validation.
	RemoveOnError bool

	// Size specifies the expected size of the file transfer if known. If the
	// server response size does not match, the transfer is cancelled and an
	// error returned.
	Size uint64

	// Hash specifies the hashing algorithm that should be used to compute the
	// trasferred file's checksum value.
	Hash hash.Hash

	// Checksum specifies the checksum value which should be compared with the
	// checksum value computed for the transferred file by the given hashing
	// algorithm.
	//
	// If the checksum values do not match, the file is deleted and an error
	// returned.
	Checksum []byte

	// NotifyOnClose specifies a channel that will notified with a pointer to
	// the Response when the transfer is completed, either successfully or with
	// an error.
	NotifyOnClose chan<- *Response

	// notifyOnCloseInternal is the same as NotifyOnClose but for private
	// internal use.
	notifyOnCloseInternal chan *Response
}

// Requests is a slice of pointers to Request structs.
type Requests []*Request

// NewRequest returns a new file transfer Request given a URL, suitable for use
// with client.Do.
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

// SetChecksum sets the request's checksum value and hashing algorithm to use
// when validating a completed file transfer.
//
// If the checksum values do not match, the file is deleted and an error
// returned.
//
// Supported hashing algoriths are supported:
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
