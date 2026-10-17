-- +goose Up
ALTER TABLE trips
    ADD COLUMN capacity          INTEGER NOT NULL DEFAULT 20 CHECK (capacity > 0),
    ADD COLUMN reserved_capacity INTEGER NOT NULL DEFAULT 0;

UPDATE trips t
SET reserved_capacity = a.units, capacity = GREATEST(t.capacity, a.units)
FROM (
    SELECT trip_id, SUM(quantity)::integer AS units
    FROM orders
    WHERE status NOT IN ('rejected', 'cancelled', 'refunded')
    GROUP BY trip_id
) a
WHERE a.trip_id = t.id;

ALTER TABLE trips
    ADD CONSTRAINT trips_reserved_capacity_check CHECK (reserved_capacity BETWEEN 0 AND capacity);

-- +goose Down
ALTER TABLE trips
    DROP COLUMN reserved_capacity,
    DROP COLUMN capacity;
