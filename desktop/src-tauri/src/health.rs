use std::io::{Read, Write};
use std::net::TcpStream;
use std::time::Duration;

use crate::models::HealthStatus;

pub const WEB_URL: &str = "http://127.0.0.1:7575/";
const WEB_ENDPOINTS: [(&str, &str); 2] = [
    ("127.0.0.1:7575", WEB_URL),
    ("[::1]:7575", "http://[::1]:7575/"),
];

pub fn check_health() -> HealthStatus {
    check_endpoints(&WEB_ENDPOINTS)
}

fn check_endpoints(endpoints: &[(&str, &str)]) -> HealthStatus {
    let mut responded = false;
    let mut last_error = String::new();
    for &(address, url) in endpoints {
        match request_path(address, "/ping") {
            Ok(buf) if is_vohive_ping_response(&buf) => {
                return health(true, url, "VoHive 后端正常".to_string());
            }
            Ok(_) => responded = true,
            Err(err) => last_error = err.to_string(),
        }
        match request_path(address, "/healthz") {
            Ok(buf) if is_vocat_liveness_response(&buf) => {
                return health(true, url, "VoCat 后端正常".to_string());
            }
            Ok(_) => responded = true,
            Err(err) => last_error = err.to_string(),
        }
    }
    if responded {
        health(
            false,
            WEB_URL,
            "端口 7575 有响应，但不像已知后端".to_string(),
        )
    } else {
        health(
            false,
            WEB_URL,
            format!("本地 IPv4/IPv6 健康检查失败: {last_error}"),
        )
    }
}

fn request_path(address: &str, path: &str) -> std::io::Result<String> {
    let mut stream = TcpStream::connect_timeout(
        &address
            .parse()
            .map_err(|err| std::io::Error::new(std::io::ErrorKind::InvalidInput, err))?,
        Duration::from_millis(800),
    )?;
    stream.set_read_timeout(Some(Duration::from_millis(800)))?;
    stream.set_write_timeout(Some(Duration::from_millis(800)))?;
    let req = format!("GET {path} HTTP/1.1\r\nHost: {address}\r\nConnection: close\r\n\r\n");
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

fn health(ok: bool, url: &str, message: String) -> HealthStatus {
    HealthStatus {
        ok,
        url: url.to_string(),
        message,
    }
}

#[cfg(test)]
mod tests {
    use super::{check_endpoints, is_vocat_liveness_response, is_vohive_ping_response};
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::thread;

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

    #[test]
    fn ipv6_vocat_is_detected_when_ipv4_is_unavailable() {
        let listener = TcpListener::bind("[::1]:0").expect("IPv6 loopback available");
        let address = listener.local_addr().unwrap().to_string();
        let url = format!("http://{address}/");
        let server = thread::spawn(move || {
            for expected_path in ["/ping", "/healthz"] {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(std::time::Duration::from_secs(2)))
                    .unwrap();
                let mut request = Vec::new();
                let mut buffer = [0; 1024];
                while !request.ends_with(b"\r\n\r\n") {
                    let bytes = stream.read(&mut buffer).unwrap();
                    assert!(bytes > 0);
                    request.extend_from_slice(&buffer[..bytes]);
                }
                assert!(String::from_utf8(request)
                    .unwrap()
                    .starts_with(&format!("GET {expected_path} ")));
                let response = if expected_path == "/ping" {
                    "HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\nnot found"
                } else {
                    "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n{\"status\":\"ok\"}"
                };
                stream.write_all(response.as_bytes()).unwrap();
            }
        });

        let result = check_endpoints(&[("127.0.0.1:0", "http://127.0.0.1:0/"), (&address, &url)]);

        server.join().unwrap();
        assert!(result.ok);
        assert_eq!(result.url, url);
        assert_eq!(result.message, "VoCat 后端正常");
    }
}
