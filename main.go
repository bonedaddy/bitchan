package main

import (
	"github.com/majestrate/bitchan/api"
	"github.com/majestrate/bitchan/db"
	"github.com/majestrate/bitchan/gossip"
	"github.com/majestrate/bitchan/model"
	"github.com/majestrate/bitchan/network"
	"github.com/majestrate/bitchan/signals"
	"github.com/majestrate/bitchan/storage"
	"github.com/majestrate/bitchan/torrent"
	"github.com/majestrate/bitchan/web"
	"github.com/sirupsen/logrus"
	"github.com/zeebo/bencode"
	"io"
	"net/http"
	"net/url"
	"os"
)

var log = logrus.New()

func newDecoder(r io.Reader) *bencode.Decoder {
	dec := bencode.NewDecoder(r)
	dec.SetFailOnUnorderedKeys(true)
	return dec
}

func main() {
	var err error
	var host string
	if len(os.Args) == 1 {
		host, err = network.LookupSelf()
	} else {
		host = os.Args[1]
		if len(os.Args) > 2 {

		}
	}
	if err != nil {
		log.WithFields(logrus.Fields{
			"error": err,
		}).Error("failed to resolve our hostname")
	}
	log.WithFields(logrus.Fields{
		"hostname": host,
	}).Info("set hostname")

	port := "8800"

	h := web.New(host, port)
	h.EnsureKeyFile("identity.key")
	h.Api = api.NewAPI()
	h.DB, err = db.NewPQ(db.Config{URL: "host=/var/run/postgresql"})
	if err != nil {
		log.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to open database")
		return
	}
	h.Api.Storage = storage.NewStorage()
	h.Api.Storage.SetRoot("file_storage")
	h.Api.Gossip = gossip.NewServer(host)
	h.Api.Torrent = torrent.NewGrabber(h.Api.Storage, h.Api.Gossip)

	s := &http.Server{
		Addr:    ":" + port,
		Handler: h,
	}
	signals.SetupSignals(func() {

	}, func() {
		s.Close()
		h.Stop()
	})
	go func() {
		h.SetupRoutes()
		log.Infof("staring up...")
		f, err := os.Open("peers.dat")
		if err == nil {
			defer f.Close()
			var list model.PeerList
			list.Peers = make(map[string]model.Peer)
			dec := newDecoder(f)
			dec.Decode(&list)
			go h.AddPeerList(list)
		}
		s.ListenAndServe()
	}()
	go func() {
		for _, torrent := range h.Api.Storage.GetAllTorrents() {
			h.Api.Torrent.Client.AddTorrentFromFile(torrent)
		}
	}()
	signals.Wait()
	log.Infof("Saving peers...")
	var list model.PeerList
	list.Peers = make(map[string]model.Peer)
	h.Api.Gossip.ForEachPeer(func(p model.Peer) {
		u, _ := url.Parse(p.URL)
		if u != nil {
			list.Peers[u.Host] = p
		}
	})
	f, err := os.Create("peers.dat")
	if err != nil {
		log.WithFields(logrus.Fields{
			"error": err,
		}).Error("failed to open peers file")
		return
	}
	defer f.Close()
	enc := bencode.NewEncoder(f)
	err = enc.Encode(&list)
	if err != nil {
		log.WithFields(logrus.Fields{
			"error": err,
		}).Error("failed to save peers file")
		return
	}
}
