# bitchan

bittorrent imageboard 

(this software is experimental and probably doesn't work rn or ever)

## build deps

* go 1.13.x
* yarn
* GNU Make
* git
* postgresql

## building

initial build:

    $ git clone https://github.com/majestrate/bitchan 
    $ cd bitchan
    $ make

running:

    $ ./bitchand your.domain.tld
    
uses port 8800 by default

add the bootstrap node (from your server server):

    $ curl http://i2p.rocks:8800/bitchan/v1/peer-with-me?host=your.domain.tld

## development

building:

    $ make mistake

clean:

    $ make repent
