#!/bin/bash

export TUT_DIR=/tmp/tmp.MH8v2FxUAE
# These are used later to locate key files.
export TUT_BARE_CA=ca
export TUT_BARE_API=apiserver

function TUT_confirmFile {
  if [ -f "$1" ]; then
    echo Got $1
  else
    echo Problem creating $1
  fi
}

function TUT_makeKeys {
  local bin=$TUT_DIR/bin

  local helmName=catalog
  local svcCatNamespace=service-catalog
  local svcCatServiceName=api

  # TODO: Document these host choices.
  local h1=${svcCatServiceName}.${svcCatNamespace}
  local h2=${svcCatServiceName}.${svcCatNamespace}.svc
  local hosts=\"$h1\",\"$h2\"

  # Generate a self-signed root certificate request bundle,
  # and have cfssljson split the bundle into the files
  # ca.csr, ca.pem, and ca-key.pem.

  cat <<EOF | \
      $bin/cfssl genkey --initca - | \
      $bin/cfssljson -bare $TUT_BARE_CA
  {
    "hosts": [ ${hosts} ],
    "key": {
        "algo": "rsa",
        "size": 2048
    },
    "names": [
        {
            "C": "US",
            "L": "san jose",
            "O": "kube",
            "OU": "WWW",
            "ST": "California"
        }
    ]
  }
EOF

  # Configure the cert generation process.
  local configFile=$(mktemp --tmpdir=$TUT_DIR)
  cat > $configFile <<EOF
  {
    "signing": { 
      "default": {
        "expiry": "43800h",
        "usages": [ "signing", "key encipherment", "server" ]
      }
    }
  }
EOF

  # FWIW, one can compare the config we'll use to the defaults:
  $bin/cfssl print-defaults config
  cat $configFile

  # Now using this self-signed certificate authority info,
  # generates a locally issued cert and key:
  cat <<EOF | \
      $bin/cfssl gencert \
          -ca=${TUT_BARE_CA}.pem \
          -ca-key=${TUT_BARE_CA}-key.pem \
          -config=$configFile - | \
      $bin/cfssljson -bare ${TUT_BARE_API}
  {
    "CN": "${svcCatServiceName}",
    "hosts": [ ${hosts} ],
    "key": {
      "algo": "rsa",
      "size": 2048
    }
  }
EOF

  echo "PKI/TLS files for use with the GCP service account:"
  echo "host1=$h1"
  echo "host2=$h2"
  TUT_confirmFile ${TUT_BARE_CA}.pem
  TUT_confirmFile ${TUT_BARE_API}.pem
  TUT_confirmFile ${TUT_BARE_API}-key.pem
}

cd $TUT_DIR
TUT_makeKeys
