variable "image" {
  type = string
}

# S3 URL of the source feed to ingest (a DO Spaces object), fetched by Nomad's
# artifact getter into the alloc so the pipeline (which reads local files only)
# can ingest it.
variable "feed_url" {
  type = string
}

# Basename of the fetched feed file (under local/feed/).
variable "feed_file" {
  type = string
}

# The vauto dealer identifier (value in the feed's DealerId column) + dealer id.
variable "dealer_id" {
  type = string
}
variable "dealer_identifier" {
  type = string
}

variable "sp_key" {
  type = string
}
variable "sp_secret" {
  type = string
}

# One-shot: fetch a source feed from Spaces and ingest it into the inventory DB
# (writer role). See scripts/deploy-pipeline.
job "feedmap-pipeline-ingest" {
  datacenters = ["dc1"]
  type        = "batch"

  group "ingest" {
    count = 1

    restart {
      attempts = 0
      mode     = "fail"
    }

    task "ingest" {
      driver = "docker"

      artifact {
        source      = var.feed_url
        destination = "local/feed/${var.feed_file}"
        mode        = "file"
        options {
          aws_access_key_id     = var.sp_key
          aws_access_key_secret = var.sp_secret
        }
      }

      config {
        # Override the image ENTRYPOINT (feedmap-pipeline) with a shell so we can
        # cd into the Nomad task dir where the artifact + config were placed.
        image      = var.image
        entrypoint = ["sh", "-lc"]
        args       = ["cd /local && feedmap-pipeline inventory capture --config config.toml --input feed/${var.feed_file} --output output --ingest --complete"]
      }

      template {
        destination = "local/config.toml"
        change_mode = "noop"
        data        = <<EOH
version = "1"
[feed]
family = "vauto"
id = "vauto:${var.dealer_identifier}"
input = "feed/${var.feed_file}"
dealer_column = "dealerid"
[dealer]
id = "${var.dealer_id}"
name = "Staging dealer ${var.dealer_id}"
identifier = "${var.dealer_identifier}"
[settings]
currency = "USD"
mileage_unit = "MI"
delimiter = ","
image_delimiter = "|"
[fields]
vin = { column = "vin", transform = "vin", required = true }
stock_number = "stock #"
year = { column = "year", transform = "integer" }
make = "make"
model = "model"
trim = "trim"
condition = { column = "newused", transform = "condition" }
exterior_color = "colour"
interior_color = "interior color"
certification = { column = "certified", transform = "boolean" }
drivetrain = "drive train"
fuel_type = "fuel_type"
body_style = "body"
images = { column = "photo url list", transform = "images", delimiter = "|" }
options = { column = "features", transform = "list", delimiter = "|" }
mileage = { column = "odometer", transform = "integer" }
source_in_stock_date = { column = "inventory date", transform = "date" }
[prices]
price = { column = "price", transform = "money" }
msrp = { column = "msrp", transform = "money" }
EOH
      }

      template {
        destination = "secrets/db.env"
        env         = true
        change_mode = "noop"
        data        = <<EOH
{{ with nomadVar "nomad/jobs/feedmap-pipeline-ingest" }}
FEEDMAP_INVENTORY_OWNER_DSN={{ .OWNER_DSN }}
FEEDMAP_INVENTORY_DSN={{ .WRITER_DSN }}
FEEDMAP_INVENTORY_READ_DSN={{ .READ_DSN }}
{{ end }}
EOH
      }

      resources {
        cpu    = 400
        memory = 512
      }
    }
  }
}
