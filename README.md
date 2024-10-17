# chosi - Change OS Image

A tool to customize a cloud-image from `cloud-images.ubuntu.com`.

## Install/Build

To install:

```bash
go install github.com/gjolly/chosi
```

To build:

```bash
go build
```

## Usage

`chosi` needs to run as `root`.

```bash
Usage of chosi:
  -cloud-config string
        Path to cloud-init config
```

Example of config file:

```json
{
    "cloudinit_config_path": "./cloud-init.yaml",
    "image_url": "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64.img",
    "extra_packages": ["./my_package.deb"],
    "kernel_version": ""
}
```

Fields:
 * `cloudinit_config_path`: Path to the cloud-init config that will be inserted in the image
 * `image_url`: URL to the QCOW2 image that should be used as base image
 * `extra_packages`: List of local `.deb` files to install in the image

Example of config for cloud-init:

```yaml
datasource_list: [ "None" ]
datasource:
  None:
    metadata:
      local-hostname: "ubuntu"
    userdata_raw: |
      #cloud-config
      chpasswd:
        expire: false
        users:
          - name: root
            password: $6$rounds=4096$.D6mj90qymJRUJcv$3LugFdGSHRPv6Wf8IVOxwq7OZjEN14mNBtjfa2KVpDkv0Qa.vV0MjbpfA46E6dpQBL7HNDFzzXyO3lJ7/nFDO1
```

This will tell cloud-init to only use the config from this file (datasource `None`), to set the hostname to `ubuntu` and the password of the user `root` as `ubuntu`. To generate a new password hash, use `mkpasswd` (e.g. `mkpasswd --method=SHA-512 --rounds=4096`).
