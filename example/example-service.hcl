job "nix-example-service" {
  datacenters = ["dc1"]
  type        = "service"

  group "example" {
    # go-httpbin: a feature-rich HTTP request/response testing service.
    task "go-httpbin" {
      driver = "nix"

      config {
        packages = [
          "#go-httpbin",
        ]
        command    = "go-httpbin"
        args       = ["-port", "8080"]
        # sandbox defaults to true
      }
    }
  }
}
