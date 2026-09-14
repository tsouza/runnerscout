output "vpc_id" {
  description = "ID of the qualification VPC. Copy straight into tools/e2e/env.example's E2E_AWS_VPC_ID."
  value       = aws_vpc.qualify.id
}

output "subnet_id" {
  description = "ID of the private subnet qualification/E2E instances actually launch into. Copy straight into tools/e2e/env.example's E2E_AWS_SUBNET_ID."
  value       = aws_subnet.private.id
}

output "security_group_id" {
  description = "ID of the qualification security group (no inbound, all outbound). Copy straight into tools/e2e/env.example's E2E_AWS_SECURITY_GROUP_ID."
  value       = aws_security_group.qualify.id
}

output "availability_zone" {
  description = "Availability Zone the private subnet (and therefore `subnet_id`) landed in. Not a separate qualify.yml/E2E input - both already derive an instance's AZ from its pinned subnet - but useful context when picking an AMI or instance type known to be available in this AZ."
  value       = aws_subnet.private.availability_zone
}
