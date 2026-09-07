use std::io::{Read, Write};
use std::net::TcpStream;
use std::time::Duration;

use crate::models::HealthStatus;

pub const WEB_URL: &str = "http://127.0.0.1:7575/";

pub fn check_health() -> HealthStatus {
    match request_path("/ping") {
        Ok(buf) if is_vohive_ping_response(&buf) => health(true, "VoHive 后端正常".to_string()),
        Ok(_) => match request_path("/healthz") {
            Ok(buf) if is_vocat_liveness_response(&buf) => {
                health(true, "VoCat 后端正常".to_string())
            }
            Ok(_) => health(false, "端口 7575 有响应，但不像已知后端".to_string()),
            Err(err) => health(false, format!("健康检查读取失败: {err}")),
        },
        Err(err) => health(false, format!("未监听: {err}")),
    }
}

fn request_path(path: &str) -> std::io::Result<String> {
    let mut stream = TcpStream::connect_timeout(
        &"127.0.0.1:7575".parse().expect("valid socket"),
        Duration::from_millis(800),
    )?;
    let _ = stream.set_read_timeout(Some(Duration::from_millis(800)));
    let req = format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
    stream.write_all(req.as_bytes())?;
    let mut buf = String::new();
    stream.read_to_string(&mut buf)?;
    Ok(buf)
}

fn is_vohive_ping_response(response: &str) -> bool {
    let mut parts = response.splitn(2, "\r\n\r\n");
    let headers = parts.next().unwrap_or_default();
    let body = parts.next().unwrap_or_default().trim();
    let status_line = headers.lines().next().unwrap_or_default();
    let status_code = status_line.split_whitespace().nth(1).unwrap_or_default();
    if status_code != "200" {
        return false;
    }
    if body == "pong" {
        return true;
    }
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|value| {
            value
                .get("message")
                .and_then(|message| message.as_str().map(|s| s == "pong"))
        })
        .unwrap_or(false)
}

fn is_vocat_liveness_response(response: &str) -> bool {
    let mut parts = response.splitn(2, "\r\n\r\n");
    let headers = parts.next().unwrap_or_default();
    let body = parts.next().unwrap_or_default().trim();
    let status_line = headers.lines().next().unwrap_or_default();
    let status_code = status_line.split_whitespace().nth(1).unwrap_or_default();
    if status_code != "200" {
        return false;
    }
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|value| {
            value
                .get("status")
                .and_then(|status| status.as_str().map(|s| s == "ok"))
        })
        .unwrap_or(false)
}

fn health(ok: bool, message: String) -> HealthStatus {
    HealthStatus {
        ok,
        url: WEB_URL.to_string(),
        message,
    }
}

#[cfg(test)]
mod tests {
    use super::{is_vocat_liveness_response, is_vohive_ping_response};

    #[test]
    fn accepts_real_vohive_ping_response() {
        let response =
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"message\":\"pong\"}";

        assert!(is_vohive_ping_response(response));
    }

    #[test]
    fn rejects_unrelated_http_200_response() {
        let response = "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nhello";

        assert!(!is_vohive_ping_response(response));
    }

    #[test]
    fn rejects_non_200_response_even_when_body_mentions_200() {
        let response = "HTTP/1.1 503 Service Unavailable\r\n\r\nretry after 200ms";

        assert!(!is_vohive_ping_response(response));
    }

    #[test]
    fn accepts_vocat_liveness_response() {
        let response =
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"status\":\"ok\"}";

        assert!(is_vocat_liveness_response(response));
    }
}
