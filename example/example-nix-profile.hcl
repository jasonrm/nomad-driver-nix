job "nix-example-profile-symlink" {
  datacenters = ["dc1"]
  type        = "service"

  # Demonstrates the nix-profile symlink. The driver creates a symlink in
  # the task's local dir pointing at the merged nix profile so templates
  # and configs can refer to package contents without baking nix store
  # paths into wrapper scripts.
  #
  # Paths use the standard Nomad ${NOMAD_TASK_DIR} / {{ env "NOMAD_TASK_DIR" }}
  # interpolations. On Linux Nomad sets these to the in-chroot path
  # (/local) since the driver advertises FSIsolationChroot; on macOS they
  # resolve to the host alloc path. Either way the same job spec works.
  #
  # nginx is fully configured from a template:
  #   - error_log uses the "stderr" keyword (inherited fd 2), so nginx never
  #     tries to open /dev/stderr — important under the macOS sandbox, which
  #     only permits writes under the task dir, /dev/null, and ttys.
  #   - all temp paths are redirected under the task dir so nginx does not
  #     try to mkdir its compiled-in defaults (/var/cache/nginx/...).
  #   - -e /dev/null suppresses the early-startup error log file that nginx
  #     opens before parsing the config.

  group "example" {
    network {
      port "http" {}
    }

    service {
      name     = "nix-example-nginx"
      provider = "nomad"
      port     = "http"

      check {
        type     = "http"
        path     = "/"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "nginx" {
      driver = "nix"

      config {
        packages = ["#nginx"]
        command  = "nginx"
        args = [
          "-c", "${NOMAD_TASK_DIR}/nginx.conf",
          "-e", "/dev/null",
          "-g", "daemon off;",
        ]
      }

      template {
        destination = "local/nginx.conf"
        change_mode = "restart"
        data        = <<EOH
worker_processes 1;
pid {{ env "NOMAD_TASK_DIR" }}/nginx.pid;
error_log stderr warn;

events { worker_connections 1024; }

http {
  include {{ env "NOMAD_TASK_DIR" }}/nix-profile/conf/mime.types;
  default_type application/octet-stream;
  access_log off;

  client_body_temp_path {{ env "NOMAD_TASK_DIR" }}/client_body_temp;
  proxy_temp_path       {{ env "NOMAD_TASK_DIR" }}/proxy_temp;
  fastcgi_temp_path     {{ env "NOMAD_TASK_DIR" }}/fastcgi_temp;
  uwsgi_temp_path       {{ env "NOMAD_TASK_DIR" }}/uwsgi_temp;
  scgi_temp_path        {{ env "NOMAD_TASK_DIR" }}/scgi_temp;

  server {
    listen {{ env "NOMAD_PORT_http" }};
    server_name _;
    location / {
      root      {{ env "NOMAD_TASK_DIR" }}/nix-profile/html;
      autoindex on;
    }
  }
}
EOH
      }
    }
  }
}
