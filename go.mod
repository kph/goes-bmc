module github.com/platinasystems/goes-bmc

require (
	github.com/platinasystems/atsock v1.1.0
	github.com/platinasystems/eeprom v1.0.0
	github.com/platinasystems/fdt v1.0.0
	github.com/platinasystems/flags v1.0.0
	github.com/platinasystems/goes v1.8.1
	github.com/platinasystems/gpio v1.0.0
	github.com/platinasystems/i2c v1.2.0
	github.com/platinasystems/log v1.2.1
	github.com/platinasystems/parms v1.0.0
	github.com/platinasystems/redis v1.2.0
	github.com/platinasystems/url v1.0.0
	github.com/snabb/tcxpgrp v0.0.0-00010101000000-000000000000 // indirect
	github.com/tatsushid/go-fastping v0.0.0-20160109021039-d7bb493dee3e
)

replace github.com/platinasystems/goes => ../goes

replace github.com/snabb/tcxpgrp => ../../kph-go/tcxpgrp
