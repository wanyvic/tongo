package adnl

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	mrand "math/rand"
	"net"
	"sync"
	"time"
)

const (
	magicTCPPing = 0x9a2b084d //crc32(tcp.ping random_id:long = tcp.Pong)
	magicTCPPong = 0x03fb69dc //crc32(tcp.pong random_id:long = tcp.Pong)
)

type Connection struct {
	cipher   cipher.Stream
	decipher cipher.Stream

	conn        net.Conn
	packetMutex sync.Mutex
	resp        chan Packet
}

func NewConnection(ctx context.Context, peerPublicKey []byte, host string) (*Connection, error) {
	a, err := NewAddress(peerPublicKey)
	if err != nil {
		return nil, err
	}
	params, err := newParameters()
	if err != nil {
		return nil, err
	}
	ci, err := aes.NewCipher(params.txKey())
	if err != nil {
		return nil, err
	}
	dci, err := aes.NewCipher(params.rxKey())
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	keys, err := newKeys(a.pubkey)
	if err != nil {
		return nil, err
	}
	var c = &Connection{
		cipher:   cipher.NewCTR(ci, params.txNonce()),
		decipher: cipher.NewCTR(dci, params.rxNonce()),
		conn:     conn,
		resp:     make(chan Packet, 1000),
	}

	err = c.handshake(a, params, keys)
	if err != nil {
		return nil, err
	}
	go c.reader()
	go c.ping()
	return c, nil
}

func (c *Connection) reader() {
	conn := bufio.NewReader(c.conn)
	for {
		p, err := ParsePacket(conn, c.decipher)
		if err != nil {
			panic(err)
		}
		if p.MagicType() == magicTCPPong {
			continue //todo: remember last pong
		}
		c.resp <- p
	}
}

func (c *Connection) handshake(address Address, params params, keys x25519Keys) error {
	key := append([]byte{}, keys.shared[:16]...)
	key = append(key, params.hash()[16:32]...)
	nonce := append([]byte{}, params.hash()[0:4]...)
	nonce = append(nonce, keys.shared[20:32]...)
	cipherKey, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	data := append([]byte{}, params[:]...)
	cipher.NewCTR(cipherKey, nonce).XORKeyStream(data, data)
	req := make([]byte, 256)
	copy(req[:32], address.hash())
	copy(req[32:64], keys.public)
	copy(req[64:96], params.hash())
	copy(req[96:], data)
	_, err = c.conn.Write(req)
	if err != nil {
		return err
	}
	_, err = ParsePacket(c.conn, c.decipher)
	if err != nil {
		return err
	}
	return nil
}

func (c *Connection) Send(p Packet) error {
	b := p.marshal()
	c.packetMutex.Lock()
	c.cipher.XORKeyStream(b, b)
	_, err := c.conn.Write(b)
	c.packetMutex.Unlock()
	return err
}

func (c *Connection) Responses() chan Packet {
	return c.resp
}

func (c *Connection) ping() {
	ping := make([]byte, 12)
	binary.BigEndian.PutUint32(ping[:4], magicTCPPing)
	for {
		time.Sleep(time.Second * 10)
		mrand.Read(ping[4:])
		p, err := NewPacket(ping)
		if err != nil {
			panic(err)
		}
		err = c.Send(p)
		if err != nil {
			panic(err)
		}
	}
}
