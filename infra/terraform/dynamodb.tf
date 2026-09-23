# The single table. Every entity in an archive shares pk = ARCH#<archiveId>,
# so one Query gets the archive and begins_with(sk, ...) selects an entity
# type:
#
#   ARCH#<aid> / META                      the archive itself
#   ARCH#<aid> / MEMBER#<accountId>        membership + role
#   ACCT#<accountId> / ARCH#<aid>          mirror, so "my archives" needs no GSI
#   ARCH#<aid> / ITEM#<itemId>             an item, files embedded
#   ARCH#<aid> / PERSON#<id>, PLACE#<id>
#   ARCH#<aid> / REL#<from>#<type>#<to>    family-tree edges
#   IDEM#<key> / META                      idempotency records (TTL'd)
#
# Only pk/sk and the GSI keys are declared here. DynamoDB is schemaless for
# every other attribute, which is exactly what lets new item types ship
# without a migration.
resource "aws_dynamodb_table" "archive" {
  name         = var.dynamodb_table_name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  attribute {
    name = "gsi1pk"
    type = "S"
  }

  attribute {
    name = "gsi1sk"
    type = "S"
  }

  # GSI1 is "the spine": gsi1pk = ARCH#<aid>#ITEM, gsi1sk = <dateSort>#<itemId>.
  #
  # The INCLUDE projection is the load-bearing decision in this whole design.
  # Because it carries tags, subjects, placeId, typeId, capabilities, the date
  # fields and the coordinates, ONE narrow Query answers filtering, faceting,
  # timeline layout, map pins and sorted pagination without ever reading the
  # base table. The base table is touched only for item detail.
  #
  # Every projected attribute is common metadata, never type-specific - which
  # is why adding an item type never requires an index change.
  #
  # "subjects" is a single list of {id, kind, name} rather than parallel
  # personIds/personNames/animalIds arrays. People and pets are one entity
  # with a kind, so one attribute serves every kind there will ever be -
  # adding vehicles or organisations later needs no index change, and neither
  # does adding a field to each entry (a "photographer" vs "depicted" role,
  # say).
  #
  # Note the projection list is the one thing here that cannot be altered in
  # place: DynamoDB requires deleting and rebuilding an index to change it.
  # That is minutes of backfill at this size, not a migration - but it is why
  # this list got a second look before the first apply.
  global_secondary_index {
    name            = "gsi1"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "INCLUDE"

    non_key_attributes = [
      "typeId",
      "typeVersion",
      "title",
      "dateSort",
      "dateEarliest",
      "dateLatest",
      "datePrecision",
      "dateBandTier",
      "lat",
      "lon",
      "confidence",
      "h3r6",
      "placeId",
      "tags",
      "subjects",
      "capabilities",
      "coverFileId",
      "coverKey",
      "coverW",
      "coverH",
      "fileCount",
      "createdAt",
      "updatedAt",
      "version",
    ]
  }

  # Reaps abandoned drafts and expired idempotency records for free. Items
  # that get committed have this attribute removed rather than extended.
  ttl {
    attribute_name = "expiresAt"
    enabled        = true
  }

  # This is an archive - the entire product promise is that things put here
  # stay here. Both of these are cheap insurance against a bad deploy or a
  # mistyped destroy.
  point_in_time_recovery {
    enabled = true
  }

  deletion_protection_enabled = true

  lifecycle {
    prevent_destroy = true
  }
}
