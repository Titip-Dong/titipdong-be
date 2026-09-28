# Feature documentation

One file per backend module: overview, business rules, database tables, endpoints, PlantUML sequence diagrams, and a
to-do list. Endpoint and table designs for modules that are not built yet are plans; once a module ships, its
migration and the generated OpenAPI spec (`/openapi.json`) are the source of truth and the file is updated to match.

| ID  | Module                              | Owner  | Status                                        |
| --- | ----------------------------------- | ------ | --------------------------------------------- |
| F01 | [Auth & User](auth.md)              | Carlo  | Partial: register, login, `GET /me`, middleware |
| F02 | [Trip & Catalog](trip.md)           | Carlo  | Not started                                   |
| F03 | [Order](order.md)                   | Alfian | Not started                                   |
| F04 | [Payment & Escrow](payment.md)      | Barru  | Not started                                   |
| F05 | [Shipment](shipment.md)             | Alfian | Not started                                   |
| F06 | [Dispute](dispute.md)               | Barru  | Not started                                   |
| F07 | [Notification](notification.md)     | Azfa   | Not started                                   |
| F08 | [Admin](admin.md)                   | Azfa   | Not started                                   |

Related: [PLAN.md](../PLAN.md) (implementation plan and open decisions), [DATABASE.md](../DATABASE.md) (full DDL,
takes precedence for table definitions), `titipdong-docs/PLAN.md` (business flow).

Diagrams are PlantUML (` ```plantuml ` blocks). Render them with the PlantUML IDE plugin, `plantuml` CLI, or
<https://www.plantuml.com/plantuml>.
