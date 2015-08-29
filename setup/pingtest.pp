# Complete setup using Puppet on DigitalOcean
# `puppet apply pingtest.pp`
# TODO(jvesuna): When pingtest is public, add git clone repo.

$local_user = "root"
$local_home = "/home/$local_user"
$paths = [ "/usr/local/bin/", "/bin/", "/sbin/" , "/usr/bin/", "/usr/sbin/" ]

user {$local_user:
     ensure => present,
}

define local_package () {
  package { "$name":
    ensure  => latest,
  }
}

define local_absent () {
  package { "$name":
    ensure => absent,
  }
}

local_package {
  # Text Editors
  'vim':;

  # Languages
  'golang-go':;

  # Networking
  'openssh-client':;
  'openssh-server':;
  'ssh':;

  # Version Control
  'git':;

  # Processes
  'htop':;

  # Misc tools
  'bash':;
  'protobuf-compiler':;
  'tmux':;
}

exec { "ensure_executable":
    command => "chmod +x ~/pingtest/golang/bin/server",
    path    => $paths,
    onlyif => "test -e /usr/bin/go"
}

exec { "build_server":
    command => "go build server",
    path    => $paths,
    onlyif => "test -e /usr/bin/go"
}
