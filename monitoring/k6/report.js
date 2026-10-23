import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const baseURL = (__ENV.BASE_URL || "").replace(/\/$/, "");
if (!baseURL) {
  throw new Error("BASE_URL is required, e.g. BASE_URL=http://host.docker.internal:8080");
}

const vusPerRole = Number(__ENV.VUS_PER_ROLE || 0);
const vus = vusPerRole > 0 ? vusPerRole * 3 : Number(__ENV.VUS || 15);
const duration = __ENV.DURATION || "1m";
const rampDuration = __ENV.RAMP_DURATION || "30s";
if (!Number.isInteger(vus) || vus < 3 || (vusPerRole > 0 && !Number.isInteger(vusPerRole))) {
  throw new Error("Set VUS to an integer of at least 3, or VUS_PER_ROLE to a positive integer");
}

const endpointErrors = new Rate("endpoint_errors");
const healthAvailability = new Rate("health_availability");
const api5xxRate = new Rate("api_5xx_rate");
const publicLatency = new Trend("public_api_latency", true);
const protectedLatency = new Trend("protected_api_latency", true);

export const options = {
  scenarios: {
    read_only_api: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { target: vus, duration: rampDuration },
        { target: vus, duration },
        { target: 0, duration: "10s" },
      ],
      gracefulRampDown: "10s",
      gracefulStop: "10s",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<1000"],
    health_availability: ["rate>0.999"],
    api_5xx_rate: ["rate<0.01"],
    public_api_latency: ["p(95)<1000"],
    protected_api_latency: ["p(95)<1000"],
    endpoint_errors: ["rate<0.01"],
  },
};

const personas = [
  { name: "buyer", email: __ENV.BUYER_EMAIL || "buyer@titipdong.test" },
  { name: "jastiper", email: __ENV.JASTIPER_EMAIL || "jastiper@titipdong.test" },
  { name: "admin", email: __ENV.ADMIN_EMAIL || "admin@titipdong.test" },
];
const password = __ENV.DEMO_PASSWORD || "demo12345";

function login(persona) {
  const response = http.post(
    `${baseURL}/auth/login`,
    JSON.stringify({ email: persona.email, password }),
    {
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      tags: { endpoint: "auth_login", persona: persona.name },
      timeout: "10s",
    },
  );
  if (response.status !== 200) {
    throw new Error(`Login for ${persona.name} failed with HTTP ${response.status}; check demo seed/accounts.`);
  }
  const body = response.json();
  if (!body || !body.accessToken) {
    throw new Error(`Login for ${persona.name} returned no access token.`);
  }
  return body.accessToken;
}

export function setup() {
  const tokens = {};
  for (const persona of personas) {
    tokens[persona.name] = login(persona);
  }
  return { tokens };
}

function get(path, endpoint, persona = "public", token = "") {
  const headers = { Accept: "application/json" };
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = http.get(`${baseURL}${path}`, {
    headers,
    tags: { endpoint, persona },
    timeout: "10s",
  });
  const ok = check(response, {
    [`${endpoint} returns 200`]: (res) => res.status === 200,
  });
  endpointErrors.add(!ok, { endpoint, persona });
  api5xxRate.add(response.status >= 500, { endpoint, persona });
  if (endpoint === "health" || endpoint === "readiness") {
    healthAvailability.add(response.status === 200, { endpoint });
  }
  (persona === "public" ? publicLatency : protectedLatency).add(response.timings.duration, {
    endpoint,
    persona,
  });
  return response;
}

function listItems(response) {
  if (response.status !== 200) return [];
  const body = response.json();
  return body && Array.isArray(body.items) ? body.items : [];
}

export default function (data) {
  // Every virtual user covers the public endpoints, then one authenticated role.
  get("/health", "health");
  get("/health/ready", "readiness");
  const tripsResponse = get("/trips?limit=20", "public_trip_list");
  const trips = listItems(tripsResponse);
  if (trips.length > 0) {
    const trip = trips[0];
    get(`/trips/${encodeURIComponent(trip.slug)}`, "public_trip_detail");
    if (trip.id) get(`/trips/${trip.id}/items`, "public_trip_catalog");
  }

  // Assign VUs round-robin to the three demo roles. All requests below are GETs.
  const persona = personas[(__VU - 1) % personas.length];
  const token = data.tokens[persona.name];
  get("/me", "current_user", persona.name, token);

  if (persona.name === "buyer") {
    const ordersResponse = get("/orders?limit=20", "buyer_orders", persona.name, token);
    get("/notifications?limit=20", "buyer_notifications", persona.name, token);
    get("/notifications/unread-count", "buyer_unread_count", persona.name, token);
    const orders = listItems(ordersResponse);
    if (orders.length > 0 && orders[0].id) {
      const orderResponse = get(`/orders/${orders[0].id}`, "buyer_order_detail", persona.name, token);
      const detail = orderResponse.status === 200 ? orderResponse.json() : null;
      if (detail && detail.order && detail.order.status === "shipped") {
        get(`/orders/${orders[0].id}/shipment`, "buyer_shipment_detail", persona.name, token);
      }
      if (detail && detail.order && detail.order.status === "disputed") {
        get(`/orders/${orders[0].id}/dispute`, "buyer_dispute_detail", persona.name, token);
      }
    }
  } else if (persona.name === "jastiper") {
    const myTripsResponse = get("/me/trips?limit=20", "jastiper_trips", persona.name, token);
    const myTrips = listItems(myTripsResponse);
    if (myTrips.length > 0 && myTrips[0].id) {
      get(`/me/trips/${myTrips[0].id}/orders?limit=20`, "jastiper_trip_orders", persona.name, token);
    }
    const ordersResponse = get("/orders?limit=20", "jastiper_orders", persona.name, token);
    get("/wallet", "jastiper_wallet", persona.name, token);
    get("/wallet/transactions?limit=20", "jastiper_wallet_transactions", persona.name, token);
    get("/payouts?limit=20", "jastiper_payouts", persona.name, token);
    get("/notifications?limit=20", "jastiper_notifications", persona.name, token);
    get("/notifications/unread-count", "jastiper_unread_count", persona.name, token);
    const orders = listItems(ordersResponse);
    if (orders.length > 0 && orders[0].id) {
      const orderResponse = get(`/orders/${orders[0].id}`, "jastiper_order_detail", persona.name, token);
      const detail = orderResponse.status === 200 ? orderResponse.json() : null;
      if (detail && detail.order && detail.order.status === "shipped") {
        get(`/orders/${orders[0].id}/shipment`, "jastiper_shipment_detail", persona.name, token);
      }
    }
  } else {
    get("/admin/stats", "admin_stats", persona.name, token);
    get("/admin/orders?limit=20", "admin_orders", persona.name, token);
    get("/admin/transactions?limit=20", "admin_transactions", persona.name, token);
    get("/admin/users?limit=20", "admin_users", persona.name, token);
    get("/admin/audit-logs?limit=20", "admin_audit_logs", persona.name, token);
    const disputesResponse = get("/admin/disputes?limit=20", "admin_disputes", persona.name, token);
    get("/admin/payouts?limit=20", "admin_payouts", persona.name, token);
    const disputes = listItems(disputesResponse);
    if (disputes.length > 0 && disputes[0].id) {
      get(`/admin/disputes/${disputes[0].id}`, "admin_dispute_detail", persona.name, token);
    }
  }

  sleep(Number(__ENV.THINK_TIME_SECONDS || 1));
}
