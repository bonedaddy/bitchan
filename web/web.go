package web

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/gin-gonic/gin"
	"github.com/majestrate/bitchan/api"
	"github.com/majestrate/bitchan/db"
	"github.com/majestrate/bitchan/gossip"
	"github.com/majestrate/bitchan/model"
	"github.com/majestrate/bitchan/util"
	"github.com/zeebo/bencode"
	"io"
	"io/ioutil"
	"lukechampine.com/blake3"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type MiddleWare struct {
	Api      *api.ApiServer
	router   *gin.Engine
	privkey  ed25519.PrivateKey
	self     model.Peer
	hostname string
	port     string
	DB       db.Facade
}

func (m *MiddleWare) AddPeerList(l model.PeerList) {
	for _, peer := range l.Peers {
		u, _ := url.Parse(peer.URL)
		if u != nil {
			go m.Api.Gossip.AddNeighboor(u)
		}
	}
}

func (m *MiddleWare) EnsureKeyFile(fname string) error {
	_, err := os.Stat(fname)
	if os.IsNotExist(err) {
		err = nil
		seed := make([]byte, 32)
		n, err := rand.Read(seed)
		if n != 32 || err != nil {
			return err
		}
		err = ioutil.WriteFile(fname, seed, os.FileMode(0600))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return err
	}
	m.privkey = ed25519.NewKeyFromSeed(data)
	return nil
}

func (m *MiddleWare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.router.ServeHTTP(w, r)
}

const BitchanPubKeyContentType = "application/x-bitchan-identity"

var b64enc = base64.StdEncoding

func encodeSig(sig []byte) string {
	return b64enc.EncodeToString(sig[:])
}

func encodePubKey(k ed25519.PublicKey) string {
	return b64enc.EncodeToString(k[:])
}

func newDecoder(r io.Reader) *bencode.Decoder {
	dec := bencode.NewDecoder(r)
	dec.SetFailOnUnorderedKeys(true)
	return dec
}

type sortedPostInfo struct {
	data []model.PostInfo
}

func (s *sortedPostInfo) Load(root string) error {
	for idx, data := range s.data {
		var meta model.Post
		fpath := filepath.Join(root, data.InfoHash+".bitchan")
		f, err := os.Open(fpath)
		if err != nil {
			return err
		}
		err = meta.ReadFromFile(f)
		if err != nil {
			return err
		}
		s.data[idx].PostedAt = meta.PostedAt
	}
	return nil
}

func (s *sortedPostInfo) Len() int {
	return len(s.data)
}

func (s *sortedPostInfo) Less(i, j int) bool {
	return s.data[j].PostedAt < s.data[i].PostedAt
}

func (s *sortedPostInfo) Swap(i, j int) {
	s.data[i], s.data[j] = s.data[j], s.data[i]
}

type sortedFileInfo struct {
	data []os.FileInfo
}

func (s *sortedFileInfo) Len() int {
	return len(s.data)
}

func (s *sortedFileInfo) Less(i, j int) bool {
	return s.data[i].ModTime().After(s.data[j].ModTime())
}

func (s *sortedFileInfo) Swap(i, j int) {
	s.data[i], s.data[j] = s.data[j], s.data[i]
}

func (m *MiddleWare) renderDirJSON(dirname string, ctx *gin.Context) {
	f, err := os.Open(dirname)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	fileInfos, err := f.Readdir(0)
	f.Close()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	infos := &sortedFileInfo{data: fileInfos[:]}
	sort.Sort(infos)
	var files []string
	for _, info := range infos.data {
		files = append(files, "/files/"+filepath.Base(dirname)+"/"+info.Name())
	}
	ctx.JSON(http.StatusOK, map[string]interface{}{"files": files})
}

func (m *MiddleWare) makeFilesURL(fname string) string {
	return "http://" + net.JoinHostPort(m.hostname, m.port) + "/files/" + filepath.Base(fname)
}

func mktmp(root, ext string) string {
	now := time.Now().UnixNano()
	var b [4]byte
	rand.Read(b[:])
	r := strings.Trim(base64.URLEncoding.EncodeToString(b[:]), "=")
	return filepath.Join(root, fmt.Sprintf("%d-%s%s", now, r, ext))
}

func (m *MiddleWare) makePost(hdr *multipart.FileHeader, text string) (p *model.Post, err error) {

	h := sha256.New()
	ext := filepath.Ext(hdr.Filename)
	tmpfile := mktmp(os.TempDir(), ext)
	inf, err := hdr.Open()
	if err != nil {
		return nil, err
	}
	defer inf.Close()
	of, err := os.Create(tmpfile)
	if err != nil {
		return nil, err
	}
	r := io.TeeReader(inf, h)
	_, err = io.Copy(of, r)
	of.Close()
	if err != nil {
		os.Remove(tmpfile)
		return nil, err
	}
	d := h.Sum(nil)
	filehash := "__" + base64.URLEncoding.EncodeToString(d[:])
	fname := filehash + ext
	real_rootf := filepath.Join(m.Api.Storage.GetRoot(), filehash)
	real_fname := filepath.Join(real_rootf, fname)
	os.Mkdir(real_rootf, os.FileMode(0700))

	tmpdir := mktmp(os.TempDir(), "")
	os.Mkdir(tmpdir, os.FileMode(0700))
	torrent_rootf := filepath.Join(tmpdir, filehash)
	os.Mkdir(torrent_rootf, os.FileMode(0700))
	torrent_fname := filepath.Join(torrent_rootf, fname)

	err = util.Move(tmpfile, torrent_fname)
	if err != nil {
		os.Remove(tmpfile)
		return nil, err
	}

	torrentFile := filepath.Join(m.Api.Storage.GetRoot(), fname+".torrent")
	torrent_txt := ""
	real_txt := ""
	if len(text) > 0 {
		text_fname := fmt.Sprintf("%s-%d.txt", m.hostname, time.Now().UnixNano())
		torrent_txt = filepath.Join(torrent_rootf, text_fname)
		real_txt = filepath.Join(real_rootf, text_fname)
		err = ioutil.WriteFile(torrent_txt, []byte(text), os.FileMode(0700))
	}
	var infohash_hex string
	if err == nil {
		infohash_hex, err = m.Api.MakeTorrent(torrent_rootf, torrentFile)
		if err == nil {
			_, err = os.Stat(real_fname)
			if os.IsNotExist(err) {
				err = util.Move(torrent_fname, real_fname)
			}
			if real_txt != "" && torrent_txt != "" {
				util.Move(torrent_txt, real_txt)
			}
		}
	}
	if err != nil {
		os.RemoveAll(tmpdir)
		os.Remove(tmpfile)
		os.Remove(fname)
		os.Remove(torrentFile)
		return nil, err
	}
	now := time.Now().UnixNano()
	p = &model.Post{
		MetaInfoURL:  m.makeFilesURL(torrentFile),
		PostedAt:     now,
		MetaInfoHash: infohash_hex,
	}
	p.Sign(m.privkey)
	go m.Api.Torrent.Grab(p.MetaInfoURL)
	metaFile := filepath.Join(m.Api.Storage.GetRoot(), infohash_hex+".bitchan")
	mf, err := os.Create(metaFile)
	if err == nil {
		err = p.WriteToFile(mf)
		mf.Close()
	}
	return p, err
}

func (m *MiddleWare) torrentURL(t *torrent.Torrent) string {
	mi := t.Metainfo()
	info, _ := mi.UnmarshalInfo()
	return m.makeFilesURL(info.Name)
}

func (m *MiddleWare) SetupRoutes() {
	m.router.LoadHTMLGlob("templates/**/*")

	// sendresult sends signed result
	sendResult := func(c *gin.Context, buf *bytes.Buffer, ct string) {
		h := blake3.New(32, nil)
		str := buf.String()
		io.Copy(h, buf)
		sig := ed25519.Sign(m.privkey, h.Sum(nil))
		c.Header("X-Bitchan-Ed25519-B3-Signature", encodeSig(sig))
		c.Header("Content-Type", ct)
		c.String(http.StatusOK, str)
	}

	m.router.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "base/index.html.tmpl", gin.H{
			"title": "bitchan on " + m.hostname,
		})
	})

	m.router.StaticFS("/static", http.Dir(filepath.Join("webroot", "static")))

	m.router.StaticFS("/files", http.Dir(m.Api.Storage.GetRoot()))

	m.router.GET("/bitchan/v1/files.json", func(c *gin.Context) {
		path := c.DefaultQuery("name", "")
		if len(path) == 0 {
			infohash_hex := c.DefaultQuery("infohash_hex", "")
			h := metainfo.NewHashFromHex(infohash_hex[:])
			t, ok := m.Api.Torrent.Client.Torrent(h)
			if !ok {
				c.JSON(http.StatusNotFound, map[string]interface{}{"error": "not found"})
				return
			}
			path = t.Name()

		}
		name := filepath.Clean(path)
		if len(name) == 0 || name == "." {
			c.JSON(http.StatusNotFound, map[string]interface{}{"error": "no name or infohash provided"})
			return
		}
		root := m.Api.Storage.GetRoot()
		m.renderDirJSON(filepath.Join(root, name), c)
	})

	m.router.GET("/bitchan/v1/posts.json", func(c *gin.Context) {
		limit_str := c.DefaultQuery("limit", "10")
		limit, _ := strconv.Atoi(limit_str)
		if limit <= 0 {
			limit = 1
		}
		if limit > 10 {
			limit = 10
		}
		var posts sortedPostInfo
		m.Api.Torrent.ForEachSeed(func(t *torrent.Torrent) {
			posts.data = append(posts.data, model.PostInfo{
				InfoHash: t.InfoHash().HexString(),
				Name:     t.Name(),
			})
		})
		err := posts.Load(m.Api.Storage.GetRoot())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": err.Error(),
			})
			return
		}
		sort.Sort(&posts)
		c.JSON(http.StatusOK, gin.H{
			"posts": posts.data,
		})
	})

	m.router.GET("/bitchan/v1/post", func(c *gin.Context) {
		infohash := c.DefaultQuery("infohash_hex", "")
		if infohash == "" {
			c.String(http.StatusNotFound, "not found")
			return
		}
		h := metainfo.NewHashFromHex(infohash[:])
		t, ok := m.Api.Torrent.Client.Torrent(h)
		if !ok {
			c.String(http.StatusNotFound, "not found")
			return
		}
		c.Redirect(http.StatusFound, m.torrentURL(t))
	})

	m.router.GET("/bitchan/v1/admin/add-peer", func(c *gin.Context) {
		rhost, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		rip := net.ParseIP(rhost)
		if !rip.IsLoopback() {
			// deny
			c.String(http.StatusForbidden, "nah")
			return
		}
		u := c.DefaultQuery("url", "")
		if u == "" {
			c.String(http.StatusBadRequest, "no url provided")
			return
		}
		remote, err := url.Parse(u)
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		if m.Api.Gossip.AddNeighboor(remote) {
			c.String(http.StatusCreated, "added")
		} else {
			c.String(http.StatusBadRequest, "not added")
		}
	})

	m.router.POST("/bitchan/v1/post", func(c *gin.Context) {

		f, err := c.FormFile("file")
		if err != nil {
			c.String(http.StatusInternalServerError, "no file provided ("+err.Error()+")")
			return
		}

		text := c.DefaultPostForm("comment", "")

		p, err := m.makePost(f, text)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		m.Api.Gossip.BroadcastLocalPost(p)
		responseType := c.DefaultQuery("t", "")
		if responseType == "plain" {
			c.Redirect(http.StatusFound, "/bitchan/v1/post?infohash_hex="+p.MetaInfoHash)
			return
		}
		if responseType == "json" {
			c.JSON(http.StatusCreated, p.ToInfo())
			return
		}
		c.Redirect(http.StatusFound, "/")
	})

	m.router.GET("/bitchan/v1/admin/bootstrap", func(c *gin.Context) {
		rhost, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
		rip := net.ParseIP(rhost)
		if !rip.IsLoopback() {
			// deny
			c.String(http.StatusForbidden, "nah")
			return
		}
		go m.Api.Gossip.Bootstrap()
		c.String(http.StatusCreated, "bootstrap started")
	})

	m.router.GET("/bitchan/v1/self", func(c *gin.Context) {
		buf := new(bytes.Buffer)
		enc := bencode.NewEncoder(buf)
		enc.Encode(m.self)
		sendResult(c, buf, gossip.HttpFeedMimeType)
	})
	m.router.GET("/bitchan/v1/pubkey", func(c *gin.Context) {
		pk := m.privkey.Public().(ed25519.PublicKey)
		c.Header("Content-Type", BitchanPubKeyContentType)
		c.String(http.StatusOK, encodePubKey(pk))
	})
	m.router.GET("/bitchan/v1/peer-with-me", func(c *gin.Context) {
		port := c.DefaultQuery("port", "8800")
		rhost, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		host := c.DefaultQuery("host", "")
		if host == "" {
			names, err := net.LookupAddr(rhost)
			if err != nil {
				c.String(http.StatusInternalServerError, err.Error())
				return
			}
			host = strings.TrimSuffix(names[0], ".")
		} else {
			addrs, err := net.LookupIP(host)
			if err != nil {
				c.String(http.StatusInternalServerError, err.Error())
				return
			}
			found := false
			for _, addr := range addrs {
				if addr.String() == rhost {
					found = true
				}
			}
			if !found {
				c.String(http.StatusForbidden, "spoofed name")
				return
			}
		}
		fedurl := "http://" + net.JoinHostPort(host, port) + "/bitchan/v1/federate"
		u, err := url.Parse(fedurl)
		if err != nil {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		if m.Api.Gossip.AddNeighboor(u) {
			c.String(http.StatusCreated, "")
		} else {
			c.String(http.StatusForbidden, "not added")
		}
	})
	m.router.POST("/bitchan/v1/federate", func(c *gin.Context) {
		ct := c.Request.Header.Get("Content-Type")
		if ct != gossip.HttpFeedMimeType {
			c.String(http.StatusForbidden, "")
			return
		}

		var p model.Post
		defer c.Request.Body.Close()
		dec := newDecoder(c.Request.Body)
		err := dec.Decode(&p)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		if !p.Verify() {
			c.String(http.StatusForbidden, "bad post signature")
			return
		}
		err = m.Api.Torrent.Grab(p.MetaInfoURL)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		var result model.PostResponse
		result.Response = "accepted"
		result.Time = time.Now().UnixNano()
		buf := new(bytes.Buffer)
		enc := bencode.NewEncoder(buf)
		enc.Encode(result)
		sendResult(c, buf, gossip.HttpFeedMimeType)
	})
	m.router.GET("/bitchan/v1/federate", func(c *gin.Context) {
		var list model.PeerList
		list.Peers = make(map[string]model.Peer)
		list.Time = time.Now().UnixNano()
		m.Api.Gossip.ForEachPeer(func(p model.Peer) {
			u, _ := url.Parse(p.URL)
			if u == nil {
				return
			}
			list.Peers[u.Host] = p
		})

		list.Peers[m.hostname] = m.self
		buf := new(bytes.Buffer)
		enc := bencode.NewEncoder(buf)
		err := enc.Encode(list)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		sendResult(c, buf, gossip.HttpFeedMimeType)
	})
}

func (m *MiddleWare) Stop() {
	m.Api.Stop()
}

func New(host string, port string) *MiddleWare {
	m := &MiddleWare{
		Api:      nil,
		router:   gin.Default(),
		hostname: host,
		port:     port,
		self: model.Peer{
			URL: "http://" + net.JoinHostPort(host, port) + "/bitchan/v1/federate",
		},
	}
	return m
}
