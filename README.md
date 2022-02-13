# Solar EV charger

The purpose of this project is to integrate a [go-eCharger](https://github.com/goecharger/go-eCharger-API-v2/blob/main/introduction-en.md) with a Victron energy invertor, running [Venus OS](https://github.com/victronenergy/venus). The integration consists in the automation of the charger, in such a way that it will always use the maximum amount of available power, after substracting any household needs.

The more power your household uses, the lower the station will be set to consume, so your batteries will not be drained by the EV charger. In ideal conditions, if your solar panel setup covers both your EV charging needs and your household, this service will always set your EV charget to maximum output. As solar irradiation changes throughout the day, so too will the charging station be adjusted to compensate, eventually turning it off once the sun goes down, and turning it back on in the morning.

This is a first release. Bugs probably exist, and there is no web base management of this service. You will need to SSH into your ColorGX to install it and/or view logs. Memory usage of this service hovers around 10 MB, but with a more aggresive garbage collection setting, it can be brought down to roughly 8 MB. 

## Building

To build the service, you will need to install [Go](https://go.dev/) and ```make```. After that, building the project is a matter of:

```bash
git clone https://github.com/gabriel-samfira/solar-ev-charger
cd solar-ev-charger
make
```

You should now have a binary in your current folder called ```solar-ev-charger```.

## Installing

We need to install this service in a way that will survive a potential VenusOS update. The only partition that is not wiped during an update is the ```/data``` partition on the device. We'll place our binary there. For simplicity, we'll assume the IP address of your ccgx is ```192.168.8.11```.

SSH into your device and create the needed folders:

```bash
mkdir -p /data/bin
mkdir -p /data/etc/solar-ev-charger
```

Copy the binary to ```ccgx```:

```bash
scp solar-ev-charger root@192.168.8.11:/data/bin/solar-ev-charger
```

Copy the sample config to the machine:

```bash
scp contrib/sample.toml root@192.168.8.11:/data/etc/solar-ev-charger/config.toml
```

Make sure you edit the sample to fit your environment.

Venus OS uses daemontools to supervise services and respawn them in case they fail. The service script for daemontools is quite simple, and is expected to exist in ```/services/``` on the color GX. A script in ```/etc/init.d``` will loop through all services present in ```/services``` and start them on boot. We will create the needed service script in the ```/data/``` partition and symlink it to ```/service```.

SSH into your ccgx and create a folder that will hold the startup script:

```bash
mkdir -p /data/etc/solar-ev-charger/service
mkdir /data/etc/solar-ev-charger/service/supervise/
```

Now create the script itself:

```bash
cat > /data/etc/solar-ev-charger/service/run << EOF
#!/bin/sh
echo "*** starting solar-ev-charger ***"
exec 2>&1

exec /data/bin/start-ev-charger.sh
```

Now make it executable:

```bash
chmod +x /data/etc/solar-ev-charger/service/run
```

Venus OS gives us the ability to customize the software running on the ccgx via two scripts:

  * ```/data/rcS.local``` runs early in the boot process
  * ```/data/rc.local``` runs late in the boot process

After an upgrade, everything except the contents of the ```/data/``` folder will be wiped. These two scripts will persist and run after upgrade, so we'll use them to make sure that our service is enabled and will start even after upgrades.

More info about these two scripts as well as everything related to root access to the device, [can be found here](https://www.victronenergy.com/live/ccgx:root_access).

For our needs, we can add our logic to ```/data/rc.local```. Add or change the script to include the following:

```bash
#!/bin/sh

EV_SERVICE_FOLDER="/data/etc/solar-ev-charger/service"
TARGET_SVC="/services/solar-ev-charger"

if [ -d "$EV_SERVICE_FOLDER" ]
then
    if [ ! -s "$TARGET_SVC" ]
    then
        # Create the service symlink
        ln -s "$EV_SERVICE_FOLDER" "$TARGET_SVC"
    fi
    # Start the service
    svc -u "$TARGET_SVC"
fi
```
Make sure the script is executable:

```bash
chmod +x /data/rc.local
```

And run it once to enable and start our service without rebooting:

```bash
/data/rc.local
```

The service should now be up and running. You can check the log file defined in the config at ```/data/etc/solar-ev-charger/config.toml```.

## Configuration

The sample config file is [fairly well commented](/contrib/sample.toml).