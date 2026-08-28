use std::io::{Read, Write};
use std::net::TcpStream;
use std::time::Duration;

fn main() {
    println!("=== Rust Integration Harness ===");
    println!("Connecting to Go API at 127.0.0.1:8080...");

    let address = "127.0.0.1:8080";
    let timeout = Duration::from_secs(2);

    match TcpStream::connect_timeout(&address.parse().unwrap(), timeout) {
        Ok(mut stream) => {
            println!("TCP Connection established! Sending HTTP request...");
            let request = "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n";

            if stream.write_all(request.as_bytes()).is_err() {
                eprintln!("ASSERTION FAILED: Failed to write to socket.");
                std::process::exit(1);
            }

            let mut response = String::new();
            if stream.read_to_string(&mut response).is_err() {
                eprintln!("ASSERTION FAILED: Failed to read from socket.");
                std::process::exit(1);
            }

            if response.contains("200 OK") && response.contains("Hello from Go API") {
                println!("{}", response);
                println!("ASSERTION SUCCESS: Go API is healthy and returned valid payload!");
            } else {
                eprintln!("ASSERTION FAILED: Unexpected response format:\n{}", response);
                std::process::exit(1);
            }
        }
        Err(e) => {
            eprintln!("ASSERTION FAILED: Could not connect to Go API: {}", e);
            std::process::exit(1);
        }
    }
}
