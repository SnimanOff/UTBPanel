// Package db provides the database/sql driver "tgpanel". It uses the core
// socket when configured, and modernc SQLite locally without a panel.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"sync"

	"github.com/SnimanOff/UTBPanel/backend/sdk"
	"github.com/SnimanOff/UTBPanel/backend/sdk/db/protocol"
	"modernc.org/sqlite"
)

func init() { sql.Register("tgpanel", &Driver{}) }

// Open uses DATABASE_URL and shares the kit's existing SDK session.
func Open(kit *sdk.Kit) (*sql.DB, error) {
	if kit == nil {
		return nil, fmt.Errorf("tgpanel db: nil kit")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("tgpanel db: DATABASE_URL is required")
	}
	return sql.OpenDB(&connector{dsn: dsn, kit: kit, remote: kit.Configured() || os.Getenv("TGPANEL_SDK") != ""}), nil
}

type Driver struct{}

func (d *Driver) Open(dsn string) (driver.Conn, error) {
	c, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

func (d *Driver) OpenConnector(dsn string) (driver.Connector, error) {
	if dsn == "" {
		return nil, fmt.Errorf("tgpanel db: empty database URL")
	}
	return &connector{dsn: dsn, remote: os.Getenv("TGPANEL_SDK") != ""}, nil
}

type connector struct {
	dsn    string
	kit    *sdk.Kit
	remote bool
	mu     sync.Mutex
	owned  *sdk.Kit
	refs   int
}

func (c *connector) Driver() driver.Driver { return &Driver{} }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if !c.remote {
		return (&sqlite.Driver{}).Open(c.dsn)
	}
	c.mu.Lock()
	k := c.kit
	if k == nil {
		if c.owned == nil {
			c.owned = sdk.New(sdk.WithoutReady(), sdk.WithModules("db"))
		}
		k = c.owned
	}
	c.refs++
	c.mu.Unlock()
	release := func() {
		c.mu.Lock()
		c.refs--
		var closeKit *sdk.Kit
		if c.refs == 0 && c.kit == nil {
			closeKit = c.owned
			c.owned = nil
		}
		c.mu.Unlock()
		if closeKit != nil {
			closeKit.Close()
		}
	}
	if err := k.Use("db"); err != nil {
		release()
		return nil, err
	}
	session, err := k.Connection(ctx)
	if err != nil {
		release()
		return nil, err
	}
	var out protocol.Opened
	if err := session.Call(ctx, "db.open", protocol.Open{Version: protocol.Version}, &out); err != nil {
		release()
		return nil, err
	}
	if out.Version != protocol.Version || out.ID == "" {
		release()
		return nil, fmt.Errorf("tgpanel db: incompatible database protocol")
	}
	return &remoteConn{session: session, id: out.ID, release: release}, nil
}
