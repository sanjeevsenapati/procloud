# ProCloud REST API Reference

All API requests require HTTP Bearer authentication:
`Authorization: Bearer <token>`

## Containers
- `GET /api/v1/containers` - List all containers
- `POST /api/v1/containers` - Create and run a new container
- `POST /api/v1/containers/{id}/start` - Start container
- `POST /api/v1/containers/{id}/stop` - Stop container
- `DELETE /api/v1/containers/{id}` - Remove container

## Schedules & Jobs
- `GET /api/v1/schedules` - List all scheduled jobs
- `POST /api/v1/schedules` - Create a schedule
- `POST /api/v1/schedules/{id}/trigger` - Run job immediately
- `DELETE /api/v1/schedules/{id}` - Delete a schedule

## DNS Service
- `GET /api/v1/dns/records` - List DNS records
- `POST /api/v1/dns/records` - Create DNS record
- `DELETE /api/v1/dns/records/{id}` - Delete DNS record
