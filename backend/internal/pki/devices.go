package pki

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

// Device request states.
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceRejected = "rejected"
	DeviceExpired  = "expired"
)

// DeviceRequest is a gate asking for a device certificate.
type DeviceRequest struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      Kind      `json:"kind"`
	IP        string    `json:"ip"`
	Code      string    `json:"code"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	cert      []byte
	csr       []byte
	serial    string
}

// Cert returns the signed certificate of an approved request.
func (d DeviceRequest) Cert() []byte { return d.cert }

// ErrNotPending is returned when approving a request that was decided.
var ErrNotPending = errors.New("pki: request is not pending")

// Enroll stores a request made with an enrollment certificate. The CSR's
// key becomes the device key once the owner approves the code.
func (ca *CA) Enroll(ctx context.Context, enroll Identity, ip string, csrDER []byte) (DeviceRequest, error) {
	if enroll.Kind != Enrollment {
		return DeviceRequest{}, errors.New("pki: enroll needs an enrollment certificate")
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return DeviceRequest{}, fmt.Errorf("pki: bad CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return DeviceRequest{}, fmt.Errorf("pki: bad CSR signature: %w", err)
	}
	var grants string
	if err := ca.db.R.QueryRowContext(ctx, `SELECT grants FROM keys WHERE serial = ?`, enroll.Serial).Scan(&grants); err != nil {
		return DeviceRequest{}, err
	}
	d := DeviceRequest{Name: enroll.Name, Kind: Kind(grants), IP: ip, Code: Code(csr.PublicKey), State: DevicePending, CreatedAt: time.Now().UTC()}
	// A gate restarted before approval asks again: replace its old request.
	err = ca.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_devices WHERE enrollment_serial = ? AND state = 'pending'`, enroll.Serial); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO pending_devices (enrollment_serial, name, kind, ip, code, csr, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			enroll.Serial, d.Name, grants, ip, d.Code, csrDER, store.FormatTime(d.CreatedAt))
		if err != nil {
			return err
		}
		d.ID, err = res.LastInsertId()
		return err
	})
	return d, err
}

func (ca *CA) scanDevices(ctx context.Context, where string, args ...any) ([]DeviceRequest, error) {
	rows, err := ca.db.R.QueryContext(ctx, `SELECT id, enrollment_serial, name, kind, ip, code, csr, state, cert, created_at FROM pending_devices `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRequest
	for rows.Next() {
		var d DeviceRequest
		var created string
		if err := rows.Scan(&d.ID, &d.serial, &d.Name, &d.Kind, &d.IP, &d.Code, &d.csr, &d.State, &d.cert, &created); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = store.ParseTime(created)
		if d.State == DevicePending && time.Since(d.CreatedAt) > EnrollmentValidity {
			d.State = DeviceExpired
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device returns one request; a gate can see only requests made with its
// own enrollment certificate.
func (ca *CA) Device(ctx context.Context, id int64, enrollSerial string) (DeviceRequest, error) {
	ds, err := ca.scanDevices(ctx, `WHERE id = ? AND enrollment_serial = ?`, id, enrollSerial)
	if err != nil {
		return DeviceRequest{}, err
	}
	if len(ds) == 0 {
		return DeviceRequest{}, ErrUnknown
	}
	return ds[0], nil
}

// PendingDevices returns requests waiting for the owner.
func (ca *CA) PendingDevices(ctx context.Context) ([]DeviceRequest, error) {
	ds, err := ca.scanDevices(ctx, `WHERE state = 'pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := ds[:0]
	for _, d := range ds {
		if d.State == DevicePending {
			out = append(out, d)
		}
	}
	return out, nil
}

// Approve signs the device certificate and retires the enrollment
// certificate, so the same gate.exe cannot register again.
func (ca *CA) Approve(ctx context.Context, id int64) (DeviceRequest, error) {
	ds, err := ca.scanDevices(ctx, `WHERE id = ?`, id)
	if err != nil {
		return DeviceRequest{}, err
	}
	if len(ds) == 0 {
		return DeviceRequest{}, ErrUnknown
	}
	d := ds[0]
	if d.State != DevicePending {
		return d, ErrNotPending
	}
	csr, err := x509.ParseCertificateRequest(d.csr)
	if err != nil {
		return d, err
	}
	if err := ca.db.Tx(ctx, func(tx *sql.Tx) error { return ca.MarkUsed(ctx, tx, d.serial) }); err != nil {
		return d, err
	}
	cert, err := ca.Issue(ctx, d.Kind, d.Name, d.IP, csr.PublicKey)
	if err != nil {
		return d, err
	}
	if _, err := ca.db.W.ExecContext(ctx, `UPDATE pending_devices SET state = 'approved', cert = ? WHERE id = ?`, cert.Raw, id); err != nil {
		return d, err
	}
	d.State, d.cert = DeviceApproved, cert.Raw
	return d, ca.Reload(ctx)
}

// Reject declines a request.
func (ca *CA) Reject(ctx context.Context, id int64) error {
	res, err := ca.db.W.ExecContext(ctx, `UPDATE pending_devices SET state = 'rejected' WHERE id = ? AND state = 'pending'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotPending
	}
	return nil
}

// PruneDevices deletes requests older than 24 hours.
func (ca *CA) PruneDevices(ctx context.Context) error {
	_, err := ca.db.W.ExecContext(ctx, `DELETE FROM pending_devices WHERE created_at < ?`, store.FormatTime(time.Now().Add(-EnrollmentValidity)))
	return err
}
