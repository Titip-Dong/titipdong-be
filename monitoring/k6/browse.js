import http from "k6/http";
import { check, sleep } from "k6";

const baseURL = __ENV.BASE_URL;

if (!baseURL) {
  throw new Error("BASE_URL is required, for example https://api.example.com");
}

export const options = {
  scenarios: {
    public_health: {
      executor: "constant-vus",
      vus: Number(__ENV.VUS || 2),
      duration: __ENV.DURATION || "30s",
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<1000"],
  },
};

export default function () {
  const response = http.get(`${baseURL}/health`);
  check(response, {
    "health returns 200": (res) => res.status === 200,
    "health returns ok": (res) => res.json("status") === "ok",
  });
  sleep(Number(__ENV.THINK_TIME_SECONDS || 1));
}
