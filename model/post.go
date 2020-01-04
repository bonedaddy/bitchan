package model

import (
	"bytes"
	"crypto/ed25519"
	"github.com/zeebo/bencode"
)

type Post struct {
	MetaInfoURL string `bencode:"bitchan-metainfo-url"`
	Version     string `bencode:"bitchan-version",omit-empty`
	PostedAt    int64  `bencode:"bitchan-posted-at"`
	PubKey      string `bencode:"bitchan-poster-pubkey"`
	Signature   string `bencode:"z",omit-empty`
}

func (p *Post) encode() []byte {
	buf := new(bytes.Buffer)
	enc := bencode.NewEncoder(buf)
	enc.Encode(p)
	return buf.Bytes()
}

func (p *Post) Verify() bool {
	sig := []byte(p.Signature)
	p.Signature = ""
	msg := p.encode()
	k := ed25519.PublicKey([]byte(p.PubKey))
	return ed25519.Verify(k, msg, sig)
}

func (p *Post) Sign(sk ed25519.PrivateKey) {
	p.PubKey = string(sk.Public().(ed25519.PublicKey)[:])
	msg := p.encode()
	p.Signature = string(ed25519.Sign(sk, msg))
}

type PostResponse struct {
	Response string `bencode:"bitchan-post-response"`
	Version  string `bencode:"bitchan-version",omit-empty`
	Time     int64  `bencode:"bitchan-time"`
}

const DefaultPostVersion = "1.0"