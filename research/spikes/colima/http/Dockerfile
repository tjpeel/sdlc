FROM busybox:1.37.0
COPY index.html /site/index.html
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/site"]
