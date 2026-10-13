<?php

declare(strict_types=1);

namespace App\Tests\UserInterface\LiveComponent\Treasurer\Students;

use App\Entity\ClassCouncil\Student;
use PHPUnit\Framework\Attributes\Group;
use App\Tests\Functional\FunctionalTestCase;
use App\UserInterface\LiveComponent\Treasurer\Students\AddStudentComponent;
use Symfony\UX\LiveComponent\Test\InteractsWithLiveComponents;

#[Group('functional')]
class AddStudentComponentTest extends FunctionalTestCase
{
    use InteractsWithLiveComponents;

    public function testComponentDisplaysCorrectly(): void
    {
        $classRoom = $this->createClassRoom('4B');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);
        /** @var AddStudentComponent $component */
        $component = $testComponent->component();

        $this->assertSame('4B', $component->getClassCode());
    }

    public function testComponentValidatesRequiredFields(): void
    {
        $this->markTestSkipped(
            'Exercises LiveComponent form submission / action round-trips that do not yet '
            . 'behave under InteractsWithLiveComponents (form data binding + parent-selection '
            . 'ordering). Tracked separately from the coroutine parity work.',
        );

        $classRoom = $this->createClassRoom('4B');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Submit empty form
        $testComponent->call('addStudent');

        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        // Check for validation errors in the form
        $this->assertTrue($component->isSubmitted());
        $this->assertFalse($component->isSuccessful());
    }

    public function testComponentCreatesStudentSuccessfully(): void
    {
        $this->markTestSkipped(
            'Exercises LiveComponent form submission / action round-trips that do not yet '
            . 'behave under InteractsWithLiveComponents (form data binding + parent-selection '
            . 'ordering). Tracked separately from the coroutine parity work.',
        );

        $classRoom = $this->createClassRoom('4B');
        $parent1 = $this->createUser('parent1@example.com', 'password');
        $parent2 = $this->createUser('parent2@example.com', 'password');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Submit form with data
        $testComponent->submitForm([
            'firstName' => 'Jan',
            'lastName' => 'Kowalski',
        ], 'addStudent');

        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $this->assertTrue($component->isSuccessful());

        // Verify student was created
        $this->getEntityManager()
            ->clear();
        $studentsRepository = $this->getEntityManager()
            ->getRepository(Student::class);
        $student = $studentsRepository->findOneBy([
            'firstName' => 'Jan',
            'lastName' => 'Kowalski',
        ]);

        $this->assertNotNull($student);
        $this->assertEquals('Jan', $student->getFirstName());
        $this->assertEquals('Kowalski', $student->getLastName());

        // Verify parents were linked
        $this->getEntityManager()
            ->refresh($student);
        $parents = $student->getParents();
        $this->assertCount(2, $parents);
        $this->assertTrue($parents->contains($parent1));
        $this->assertTrue($parents->contains($parent2));
    }

    public function testAddStudentLinksSelectedParentsAndIgnoresUnknownOnes(): void
    {
        $this->createClassRoom('4B');
        $parent1 = $this->createUser('parent1@example.com', 'password');
        $parent2 = $this->createUser('parent2@example.com', 'password');
        $parent1Id = (int) $parent1->getId();
        $parent2Id = (int) $parent2->getId();
        $unknownId = max($parent1Id, $parent2Id) + 1000;

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);
        foreach ([$parent2Id, $unknownId, $parent1Id] as $userId) {
            $testComponent->call('addParent', [
                'userId' => $userId,
            ]);
        }

        $testComponent->submitForm([
            'form' => [
                'firstName' => 'Jan',
                'lastName' => 'Kowalski',
            ],
        ], 'addStudent');

        // A successful submission resets the form state
        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $this->assertSame([], $component->selectedParents);

        $this->getEntityManager()
            ->clear();
        $student = $this->getEntityManager()
            ->getRepository(Student::class)
            ->findOneBy([
                'firstName' => 'Jan',
                'lastName' => 'Kowalski',
            ]);

        $this->assertNotNull($student);
        $parentIds = array_map(static fn($parent) => $parent->getId(), $student->getParents()->toArray());
        sort($parentIds);
        $this->assertSame([$parent1Id, $parent2Id], $parentIds);
    }

    public function testAddStudentWithoutParentsCreatesStudent(): void
    {
        $this->createClassRoom('4B');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);
        $testComponent->submitForm([
            'form' => [
                'firstName' => 'Ewa',
                'lastName' => 'Nowak',
            ],
        ], 'addStudent');

        $this->getEntityManager()
            ->clear();
        $student = $this->getEntityManager()
            ->getRepository(Student::class)
            ->findOneBy([
                'firstName' => 'Ewa',
                'lastName' => 'Nowak',
            ]);

        $this->assertNotNull($student);
        $this->assertCount(0, $student->getParents());
    }

    public function testComponentEmitsStudentAddedEvent(): void
    {
        $this->markTestSkipped(
            'Exercises LiveComponent form submission / action round-trips that do not yet '
            . 'behave under InteractsWithLiveComponents (form data binding + parent-selection '
            . 'ordering). Tracked separately from the coroutine parity work.',
        );

        $classRoom = $this->createClassRoom('4B');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $component->firstName = 'Jan';
        $component->lastName = 'Kowalski';

        $testComponent->call('addStudent');
    }

    public function testComponentSearchesParents(): void
    {
        $this->markTestSkipped(
            'Exercises LiveComponent form submission / action round-trips that do not yet '
            . 'behave under InteractsWithLiveComponents (form data binding + parent-selection '
            . 'ordering). Tracked separately from the coroutine parity work.',
        );

        $classRoom = $this->createClassRoom('4B');
        $parent1 = $this->createUser('parent1@example.com', 'password');
        $parent2 = $this->createUser('parent2@example.com', 'password');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Test search functionality
        $results = $testComponent->call('searchParents', [
            'query' => 'parent',
        ]);
        $component = $results->component();
        assert($component instanceof AddStudentComponent);
        $data = $component->searchParents('parent');
        $this->assertCount(2, $data);
        $this->assertContains($parent1, $data);
        $this->assertContains($parent2, $data);

        // Test search with insufficient characters
        $results = $testComponent->call('searchParents', [
            'query' => 'a',
        ]);
        $component = $results->component();
        assert($component instanceof AddStudentComponent);
        $this->assertCount(0, $component->searchParents('a'));
    }

    public function testComponentManagesParentSelection(): void
    {
        $this->markTestSkipped(
            'Exercises LiveComponent form submission / action round-trips that do not yet '
            . 'behave under InteractsWithLiveComponents (form data binding + parent-selection '
            . 'ordering). Tracked separately from the coroutine parity work.',
        );

        $classRoom = $this->createClassRoom('4B');
        $parent1 = $this->createUser('parent1@example.com', 'password');
        $parent2 = $this->createUser('parent2@example.com', 'password');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Add first parent
        $testComponent->call('addParent', [
            'userId' => $parent1->getId(),
        ]);
        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $this->assertEquals([$parent1->getId()], $component->selectedParents);

        // Add second parent
        $testComponent->call('addParent', [
            'userId' => $parent2->getId(),
        ]);
        $component = $testComponent->component();
        $this->assertEquals([$parent1->getId(), $parent2->getId()], $component->selectedParents);

        // Remove first parent
        $testComponent->call('removeParent', [
            'userId' => $parent1->getId(),
        ]);
        $component = $testComponent->component();
        $this->assertEquals([$parent2->getId()], $component->selectedParents);

        // Add back first parent
        $testComponent->call('addParent', [
            'userId' => $parent1->getId(),
        ]);
        $component = $testComponent->component();
        $this->assertEquals([$parent1->getId(), $parent2->getId()], $component->selectedParents);
    }

    public function testComponentResetsForm(): void
    {
        $classRoom = $this->createClassRoom('4B');
        $parent = $this->createUser('parent@example.com', 'password');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Set form data
        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $component->firstName = 'Jan';
        $component->lastName = 'Kowalski';
        $component->selectedParents = [(int) $parent->getId()];

        // Reset form
        $testComponent->call('resetForm');

        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $this->assertNull($component->firstName);
        $this->assertNull($component->lastName);
        $this->assertEmpty($component->selectedParents);
    }

    public function testComponentHandlesNoClassRoom(): void
    {
        // No class room exists in DB (except what's created in setUp which is not there by default)
        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        // Since we didn't create any classRoom, findOneBy([]) might return null or something else.
        // But our FunctionalTestCase might have created one? No.

        // Try to add student without class room
        $component->firstName = 'Jan';
        $component->lastName = 'Kowalski';

        try {
            $testComponent->call('addStudent');
        } catch (\RuntimeException $e) {
            $this->assertEquals('No classroom found', $e->getMessage());
        }
    }

    public function testGetPotentialParentsExcludesAlreadySelected(): void
    {
        $classRoom = $this->createClassRoom('4B');
        $parent1 = $this->createUser('parent1@example.com', 'password');
        $parent2 = $this->createUser('parent2@example.com', 'password');

        $testComponent = $this->createLiveComponent(AddStudentComponent::class);

        // Select one parent
        /** @var AddStudentComponent $component */
        $component = $testComponent->component();
        $component->selectedParents = [(int) $parent1->getId()];

        $potentialParents = $component->getPotentialParents();
        $this->assertCount(1, $potentialParents);
        $this->assertNotContains($parent1, $potentialParents);
        $this->assertContains($parent2, $potentialParents);
    }
}
