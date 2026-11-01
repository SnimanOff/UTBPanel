module github.com/SnimanOff/UTBPanel/backend

go 1.27

require (
	filippo.io/age v1.3.2
	fyne.io/systray v1.12.0
	github.com/SnimanOff/UTBPanel/backend/sdk v0.0.0
	github.com/axiomhq/hyperloglog v0.3.0
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/robfig/cron/v3 v3.0.1
	github.com/sethvargo/go-diceware v0.6.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
	modernc.org/sqlite v1.61.1
)

require (
	filippo.io/hpke v0.4.0 // indirect
	github.com/dgryski/go-metro v0.0.0-20250106013310-edb8663e5e33 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/kamstrup/intmap v0.5.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/SnimanOff/UTBPanel/backend/sdk => ./sdk
