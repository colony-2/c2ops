The package.json, bin/cli.js and scripts/postinstall.js fixtures are unchanged
from colony-2/c2j commit c412b478897ee0637f879b3c7b13b1afbdf092a6, npm/c2j/.
They exercise the product's actual GitHub URLs, checksum verification and bin
conventions. All test HTTP traffic is routed to independent loopback fixtures;
these tests never publish the production package. Fixture binary archives use a
small executable shell script rather than claiming Go/architecture acceptance.
